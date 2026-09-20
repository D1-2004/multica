package wsfs

import "testing"

func TestJailRelPathRejectsTraversal(t *testing.T) {
	if _, err := JailRelPath("files/../home"); err == nil {
		t.Fatal("accepted parent traversal")
	}
	if _, err := JailRelPath(".."); err == nil {
		t.Fatal("accepted ..")
	}
	if _, err := JailFileName("../x"); err == nil {
		t.Fatal("accepted traversal filename")
	}
	if _, err := JailFileName("a/b"); err == nil {
		t.Fatal("accepted slash in filename")
	}
	got, err := JailRelPath("/docs/readme.md")
	if err != nil || got != "docs/readme.md" {
		t.Fatalf("got %q err=%v", got, err)
	}
	got, err = JailRelPath("/")
	if err != nil || got != "." {
		t.Fatalf("root %q err=%v", got, err)
	}
}

func TestPageDirectorySortsThenSlices(t *testing.T) {
	entries := []DirEntry{
		{Name: "b.txt", IsDir: false},
		{Name: "a", IsDir: true},
		{Name: "c", IsDir: true},
		{Name: "a.txt", IsDir: false},
	}
	page, truncated, next, err := PageDirectory(entries, 0, 2)
	if err != nil || !truncated || next != 2 || len(page) != 2 || page[0].Name != "a" || page[1].Name != "c" {
		t.Fatalf("page=%+v truncated=%v next=%d err=%v", page, truncated, next, err)
	}
	page, truncated, next, err = PageDirectory(entries, 2, 2)
	if err != nil || truncated || next != 4 || page[0].Name != "a.txt" || page[1].Name != "b.txt" {
		t.Fatalf("second page=%+v truncated=%v next=%d err=%v", page, truncated, next, err)
	}
}

func TestPageDirectoryRejectsHugeDirectory(t *testing.T) {
	entries := make([]DirEntry, MaxDirectorySize+1)
	if _, _, _, err := PageDirectory(entries, 0, 200); err != ErrDirectoryTooLarge {
		t.Fatalf("err=%v", err)
	}
}
