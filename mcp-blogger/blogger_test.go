package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

func withTestServer(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	old := apiBase
	apiBase = srv.URL
	t.Cleanup(func() { apiBase = old })
}

func testClient() *bloggerClient {
	return &bloggerClient{googleapi.New(&http.Client{Timeout: 5 * time.Second}, "Blogger API")}
}

const twoBlogs = `{"items":[
	{"id":"111","name":"Alpha","url":"http://alpha.blogspot.com/"},
	{"id":"222","name":"Beta","url":"https://www.beta.example/"}
],"blogUserInfos":[{"blog_user_info":{"role":"ADMIN"}},{"blog_user_info":{"role":"AUTHOR"}}]}`

func TestResolveBlog(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users/self/blogs":
			w.Write([]byte(twoBlogs))
		case "/blogs/byurl":
			if r.URL.Query().Get("url") == "https://other.blogspot.com/" {
				w.Write([]byte(`{"id":"333"}`))
				return
			}
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"code":404,"message":"Not Found"}}`))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	})
	c := testClient()
	ctx := context.Background()
	cases := map[string]string{
		"999":                           "999",
		"alpha.blogspot.com":            "111",
		"https://alpha.blogspot.com/x":  "111",
		"beta.example":                  "222",
		"http://www.beta.example":       "222",
		"other.blogspot.com":            "333",
		" https://Alpha.Blogspot.com/ ": "111",
	}
	for in, want := range cases {
		got, err := c.resolveBlog(ctx, in)
		if err != nil || got != want {
			t.Errorf("resolveBlog(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := c.resolveBlog(ctx, ""); err == nil || !strings.Contains(err.Error(), "2 blogs") {
		t.Errorf("empty input with 2 blogs: err = %v", err)
	}
	if _, err := c.resolveBlog(ctx, "nope.example"); err == nil || !strings.Contains(err.Error(), "Alpha") {
		t.Errorf("unknown blog: err = %v", err)
	}
}

func TestResolveBlogSingle(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"items":[{"id":"42","name":"Only"}]}`))
	})
	got, err := testClient().resolveBlog(context.Background(), "")
	if err != nil || got != "42" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestListBlogsFallsBackWithoutAdminView(t *testing.T) {
	var calls int
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("view") == "ADMIN" {
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(`{"error":{"code":403,"message":"forbidden"}}`))
			return
		}
		w.Write([]byte(twoBlogs))
	})
	blogs, infos, err := testClient().listBlogs(context.Background())
	if err != nil || len(blogs) != 2 || infos[1].Role != "AUTHOR" || calls != 2 {
		t.Fatalf("blogs=%v infos=%v err=%v calls=%d", blogs, infos, err, calls)
	}
}

func TestResolvePostByURL(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/blogs/111/posts/bypath" || r.URL.Query().Get("path") != "/2026/10/hello.html" {
			t.Errorf("unexpected request %s", r.URL)
		}
		w.Write([]byte(`{"id":"777"}`))
	})
	c := testClient()
	got, err := c.resolvePost(context.Background(), "111", "https://alpha.blogspot.com/2026/10/hello.html?m=1")
	if err != nil || got != "777" {
		t.Fatalf("got %q, %v", got, err)
	}
	if got, _ := c.resolvePost(context.Background(), "111", "123"); got != "123" {
		t.Errorf("numeric id not passed through: %q", got)
	}
	if _, err := c.resolvePost(context.Background(), "111", "hello"); err == nil {
		t.Error("bare slug should be rejected")
	}
}

func TestResolvePageByURL(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query()["status"]; len(got) != 2 {
			t.Errorf("status = %v, want LIVE and DRAFT", got)
		}
		w.Write([]byte(`{"items":[
			{"id":"1","url":"http://alpha.blogspot.com/p/about.html"},
			{"id":"2","url":"http://alpha.blogspot.com/p/contact.html"}
		]}`))
	})
	got, err := testClient().resolvePage(context.Background(), "111", "https://alpha.blogspot.com/p/contact.html")
	if err != nil || got != "2" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestAPITime(t *testing.T) {
	cases := []struct {
		in   string
		end  bool
		want string
	}{
		{"", false, ""},
		{"2026-10-01", false, "2026-10-01T00:00:00Z"},
		{"2026-10-31", true, "2026-11-01T00:00:00Z"},
		{"2026-10-31T09:00:00+07:00", true, "2026-10-31T09:00:00+07:00"},
	}
	for _, c := range cases {
		if got, err := apiTime(c.in, c.end); err != nil || got != c.want {
			t.Errorf("apiTime(%q, %v) = %q, %v; want %q", c.in, c.end, got, err, c.want)
		}
	}
	if _, err := apiTime("31/10/2026", false); err == nil {
		t.Error("bad date accepted")
	}
}

func TestPublishTime(t *testing.T) {
	if _, err := publishTime("2026-11-05"); err == nil {
		t.Error("date without time accepted")
	}
	if got, err := publishTime("2026-11-05T08:00:00+07:00"); err != nil || got == "" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestEnumArgAndStatuses(t *testing.T) {
	if v, err := enumArg("orderBy", "updated", "PUBLISHED", "UPDATED"); err != nil || v != "UPDATED" {
		t.Errorf("got %q, %v", v, err)
	}
	q := url.Values{}
	if err := addStatuses(q, []string{"live"}, postStatuses); err != nil || q.Has("view") {
		t.Errorf("LIVE only: q=%v err=%v", q, err)
	}
	if err := addStatuses(q, []string{"draft"}, postStatuses); err != nil || q.Get("view") != "ADMIN" || len(q["status"]) != 2 {
		t.Errorf("with DRAFT: q=%v err=%v", q, err)
	}
	if err := addStatuses(url.Values{}, []string{"SCHEDULED"}, pageStatuses); err == nil {
		t.Error("SCHEDULED is not a page status")
	}
}

func TestListQuery(t *testing.T) {
	q, err := listQuery(0, "tok", false)
	if err != nil || q.Get("maxResults") != "20" || q.Get("pageToken") != "tok" || q.Get("fetchBodies") != "false" {
		t.Errorf("q=%v err=%v", q, err)
	}
	if _, err := listQuery(501, "", false); err == nil {
		t.Error("maxResults 501 accepted")
	}
}

func TestPatchBody(t *testing.T) {
	if _, err := patchBody("", "", "", nil); err == nil {
		t.Error("empty patch accepted")
	}
	empty := []string{}
	p, err := patchBody("", "", "", &empty)
	if err != nil || p["labels"] == nil {
		t.Errorf("clearing labels: %v, %v", p, err)
	}
	dir := t.TempDir()
	f := filepath.Join(dir, "body.html")
	os.WriteFile(f, []byte("<p>hi</p>"), 0o644)
	p, err = patchBody("T", "", f, nil)
	if err != nil || p["content"] != "<p>hi</p>" || p["title"] != "T" {
		t.Errorf("from file: %v, %v", p, err)
	}
	if _, err := patchBody("", "<p>x</p>", f, nil); err == nil {
		t.Error("content and contentFile together accepted")
	}
}

func TestSendDecodesWrite(t *testing.T) {
	withTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/blogs/111/posts" || r.URL.Query().Get("isDraft") != "true" {
			t.Errorf("unexpected %s %s", r.Method, r.URL)
		}
		w.Write([]byte(`{"id":"9","title":"Hi","status":"DRAFT"}`))
	})
	var p post
	err := testClient().send(context.Background(), http.MethodPost, blogPath("111")+"/posts",
		url.Values{"isDraft": {"true"}}, map[string]any{"title": "Hi"}, &p)
	if err != nil || p.ID != "9" || p.Status != "DRAFT" {
		t.Fatalf("p=%+v err=%v", p, err)
	}
	if !strings.Contains(renderPost(p, false), "Status: DRAFT") {
		t.Error("renderPost misses status")
	}
}
