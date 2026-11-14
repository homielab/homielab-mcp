package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/homielab/homielab-mcp/internal/googleapi"
)

var apiBase = "https://blogger.googleapis.com/v3"

type bloggerClient struct {
	*googleapi.Client
}

func newBloggerClient(ctx context.Context) (*bloggerClient, error) {
	hc, err := auth.HTTPClient(ctx)
	if err != nil {
		return nil, err
	}
	return &bloggerClient{googleapi.New(hc, "Blogger API")}, nil
}

// ---- resources ----

type author struct {
	ID          string `json:"id"`
	DisplayName string `json:"displayName"`
	URL         string `json:"url"`
}

type blog struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Published   string `json:"published"`
	Updated     string `json:"updated"`
	Status      string `json:"status"`
	Posts       struct {
		TotalItems int `json:"totalItems"`
	} `json:"posts"`
	Pages struct {
		TotalItems int `json:"totalItems"`
	} `json:"pages"`
	Locale struct {
		Language string `json:"language"`
		Country  string `json:"country"`
	} `json:"locale"`
}

type blogUserInfo struct {
	Role           string `json:"role"`
	HasAdminAccess bool   `json:"hasAdminAccess"`
}

type post struct {
	ID             string   `json:"id"`
	Title          string   `json:"title"`
	Content        string   `json:"content,omitempty"`
	URL            string   `json:"url"`
	Status         string   `json:"status"`
	Published      string   `json:"published"`
	Updated        string   `json:"updated"`
	Labels         []string `json:"labels"`
	Author         author   `json:"author"`
	ReaderComments string   `json:"readerComments"`
	Replies        struct {
		TotalItems string `json:"totalItems"`
	} `json:"replies"`
}

type page struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Content   string `json:"content,omitempty"`
	URL       string `json:"url"`
	Status    string `json:"status"`
	Published string `json:"published"`
	Updated   string `json:"updated"`
	Author    author `json:"author"`
}

type comment struct {
	ID        string `json:"id"`
	Content   string `json:"content"`
	Status    string `json:"status"`
	Published string `json:"published"`
	Author    author `json:"author"`
	Post      struct {
		ID string `json:"id"`
	} `json:"post"`
	InReplyTo *struct {
		ID string `json:"id"`
	} `json:"inReplyTo"`
}

type pageviews struct {
	Counts []struct {
		TimeRange string `json:"timeRange"`
		Count     string `json:"count"`
	} `json:"counts"`
}

// listPage is one page of a list response.
type listPage[T any] struct {
	Items         []T    `json:"items"`
	NextPageToken string `json:"nextPageToken"`
}

// ---- API calls ----

func (c *bloggerClient) getJSON(ctx context.Context, path string, q url.Values, out any) error {
	u := googleapi.JoinURL(apiBase, path)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	body, _, err := c.Do(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// send runs a write (POST/PUT/PATCH/DELETE) and decodes the response into
// out when out is non-nil.
func (c *bloggerClient) send(ctx context.Context, method, path string, q url.Values, body, out any) error {
	u := googleapi.JoinURL(apiBase, path)
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	data, _, err := c.Do(ctx, method, u, body)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func blogPath(blogID string) string { return "/blogs/" + url.PathEscape(blogID) }

// listBlogs lists the user's blogs with the user's role on each.
func (c *bloggerClient) listBlogs(ctx context.Context) ([]blog, []blogUserInfo, error) {
	var out struct {
		Items         []blog `json:"items"`
		BlogUserInfos []struct {
			Info blogUserInfo `json:"blog_user_info"`
		} `json:"blogUserInfos"`
	}
	q := url.Values{"fetchUserInfo": {"true"}, "view": {"ADMIN"}}
	if err := c.getJSON(ctx, "/users/self/blogs", q, &out); err != nil {
		// view=ADMIN fails when the user is only an author on some blog.
		q.Del("view")
		if err2 := c.getJSON(ctx, "/users/self/blogs", q, &out); err2 != nil {
			return nil, nil, err
		}
	}
	infos := make([]blogUserInfo, len(out.Items))
	for i := range infos {
		if i < len(out.BlogUserInfos) {
			infos[i] = out.BlogUserInfos[i].Info
		}
	}
	return out.Items, infos, nil
}

func (c *bloggerClient) getBlog(ctx context.Context, blogID string) (blog, error) {
	var b blog
	err := c.getJSON(ctx, blogPath(blogID), nil, &b)
	return b, err
}

func (c *bloggerClient) getBlogByURL(ctx context.Context, blogURL string) (blog, error) {
	var b blog
	err := c.getJSON(ctx, "/blogs/byurl", url.Values{"url": {blogURL}}, &b)
	return b, err
}

func (c *bloggerClient) getPost(ctx context.Context, blogID, postID string, q url.Values) (post, error) {
	var p post
	err := c.getJSON(ctx, blogPath(blogID)+"/posts/"+url.PathEscape(postID), q, &p)
	return p, err
}

func (c *bloggerClient) getPostByPath(ctx context.Context, blogID, path string) (post, error) {
	var p post
	err := c.getJSON(ctx, blogPath(blogID)+"/posts/bypath", url.Values{"path": {path}}, &p)
	return p, err
}

func (c *bloggerClient) getPage(ctx context.Context, blogID, pageID string, q url.Values) (page, error) {
	var p page
	err := c.getJSON(ctx, blogPath(blogID)+"/pages/"+url.PathEscape(pageID), q, &p)
	return p, err
}

// ---- resolution ----

// resolveBlog maps user input to a blog id: a numeric id is used as is; a
// URL or host ("myblog.blogspot.com", "https://www.example.com/") is matched
// against the user's blogs, then looked up with blogs.getByUrl; empty input
// picks the only blog, or errors with the list.
func (c *bloggerClient) resolveBlog(ctx context.Context, in string) (string, error) {
	in = strings.TrimSpace(in)
	if isID(in) {
		return in, nil
	}
	blogs, _, err := c.listBlogs(ctx)
	if err != nil {
		return "", err
	}
	if in == "" {
		switch len(blogs) {
		case 0:
			return "", fmt.Errorf("no blogs found for this Google login")
		case 1:
			return blogs[0].ID, nil
		}
		return "", fmt.Errorf("%d blogs accessible; pass blogId. Available: %s", len(blogs), blogList(blogs))
	}
	host := hostOf(in)
	for _, b := range blogs {
		if hostOf(b.URL) == host {
			return b.ID, nil
		}
	}
	b, err := c.getBlogByURL(ctx, "https://"+host+"/")
	if err != nil {
		return "", fmt.Errorf("no blog matches %q (%v). Available: %s", in, err, blogList(blogs))
	}
	return b.ID, nil
}

// resolvePost maps a post id or post URL to a post id.
func (c *bloggerClient) resolvePost(ctx context.Context, blogID, in string) (string, error) {
	in = strings.TrimSpace(in)
	if isID(in) {
		return in, nil
	}
	path, err := urlPath(in)
	if err != nil {
		return "", fmt.Errorf("postId must be a numeric id or the post URL: %w", err)
	}
	p, err := c.getPostByPath(ctx, blogID, path)
	if err != nil {
		return "", fmt.Errorf("no post at %s: %w", path, err)
	}
	return p.ID, nil
}

// resolvePage maps a page id or page URL (…/p/name.html) to a page id.
func (c *bloggerClient) resolvePage(ctx context.Context, blogID, in string) (string, error) {
	in = strings.TrimSpace(in)
	if isID(in) {
		return in, nil
	}
	path, err := urlPath(in)
	if err != nil {
		return "", fmt.Errorf("pageId must be a numeric id or the page URL: %w", err)
	}
	pages, err := googleapi.ListAll[page](ctx, c.Client, googleapi.JoinURL(apiBase, blogPath(blogID)+"/pages"),
		url.Values{"fetchBodies": {"false"}, "status": {"LIVE", "DRAFT"}}, "items")
	if err != nil {
		return "", err
	}
	for _, p := range pages {
		if pp, err := urlPath(p.URL); err == nil && pp == path {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("no page at %s", path)
}

func isID(s string) bool {
	if s == "" {
		return false
	}
	_, err := strconv.ParseUint(s, 10, 64)
	return err == nil
}

// hostOf returns the lowercase host of a URL or bare host, without "www.".
func hostOf(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(u.Hostname(), "www.")
}

// urlPath returns the path of a full URL ("/2026/10/my-post.html").
func urlPath(s string) (string, error) {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Path == "" || u.Path == "/" {
		return "", fmt.Errorf("not a post or page URL: %q", s)
	}
	return u.Path, nil
}

func blogList(blogs []blog) string {
	names := make([]string, len(blogs))
	for i, b := range blogs {
		names[i] = fmt.Sprintf("%s (%s, id %s)", b.Name, b.URL, b.ID)
	}
	return strings.Join(names, ", ")
}

// ---- argument helpers ----

// apiTime turns YYYY-MM-DD or RFC 3339 into RFC 3339. A bare date is the
// start of that day (UTC), or the start of the next day when end is true, so
// an end date includes the whole day.
func apiTime(s string, end bool) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if d, err := time.Parse(time.DateOnly, s); err == nil {
		if end {
			d = d.AddDate(0, 0, 1)
		}
		return d.Format(time.RFC3339), nil
	}
	if _, err := time.Parse(time.RFC3339, s); err == nil {
		return s, nil
	}
	return "", fmt.Errorf("date %q must be YYYY-MM-DD or RFC 3339 (2026-10-31T09:00:00+07:00)", s)
}

// enumArg upper-cases v and checks it against allowed (empty is allowed).
func enumArg(name, v string, allowed ...string) (string, error) {
	if v == "" {
		return "", nil
	}
	u := strings.ToUpper(strings.TrimSpace(v))
	for _, a := range allowed {
		if strings.ToUpper(a) == u {
			return a, nil
		}
	}
	return "", fmt.Errorf("%s %q must be one of %s", name, v, strings.Join(allowed, ", "))
}

// contentArg returns the HTML body from content or, when set, contentFile.
func contentArg(content, contentFile string) (string, error) {
	if contentFile == "" {
		return content, nil
	}
	if content != "" {
		return "", fmt.Errorf("pass content or contentFile, not both")
	}
	data, err := os.ReadFile(contentFile)
	if err != nil {
		return "", err
	}
	return string(data), nil
}
