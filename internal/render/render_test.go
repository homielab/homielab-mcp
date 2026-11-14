package render

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCells(t *testing.T) {
	if got := MDCell("a|b\nc"); got != `a\|b c` {
		t.Errorf("MDCell = %q", got)
	}
	if got := CSVLine([]string{"plain", `say "hi"`, "a,b", ""}); got != "plain,\"say \"\"hi\"\"\",\"a,b\",\n" {
		t.Errorf("CSVLine = %q", got)
	}
	if OrDash("") != "—" || OrDash("x") != "x" {
		t.Error("OrDash")
	}
}

func TestSaveCSV(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "out.csv")
	csv := "h\n1\n2\n3\n4\n5\n6\n7\n"
	msg, err := SaveCSV(path, csv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(msg, "7 data rows + header") || !strings.Contains(msg, "... (2 more rows in the file)") {
		t.Errorf("msg = %q", msg)
	}
	if data, _ := os.ReadFile(path); string(data) != csv {
		t.Error("file content differs")
	}
	if _, err := SaveCSV(filepath.Join(path, "x.csv"), csv); err == nil {
		t.Error("writing under a file should fail")
	}
}
