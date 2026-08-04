package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/nna774/s.nna774.net/datastore"
	"github.com/nna774/s.nna774.net/web"
)

func TestStatusesRange(t *testing.T) {
	cases := []struct {
		page     int
		perPage  int
		wantTake int
		wantSkip int
	}{
		{page: 1, perPage: 20, wantTake: 21, wantSkip: 0},
		{page: 2, perPage: 20, wantTake: 41, wantSkip: 20},
		{page: 3, perPage: 5, wantTake: 16, wantSkip: 10},
	}
	for _, c := range cases {
		take, skip := statusesRange(c.page, c.perPage)
		if take != c.wantTake || skip != c.wantSkip {
			t.Errorf("statusesRange(%v, %v) = (%v, %v), want (%v, %v)",
				c.page, c.perPage, take, skip, c.wantTake, c.wantSkip)
		}
	}
}

func entriesOf(ids ...int) []datastore.Entry {
	es := make([]datastore.Entry, 0, len(ids))
	for _, id := range ids {
		es = append(es, datastore.Entry{ID: id})
	}
	return es
}

func idsOf(entries []datastore.Entry) []int {
	ids := make([]int, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	return ids
}

func TestStatusesSlice(t *testing.T) {
	cases := []struct {
		name        string
		entries     []datastore.Entry
		skip        int
		perPage     int
		wantIDs     []int
		wantHasNext bool
	}{
		{
			// perPage+1 件取れた = 次のページがある。
			name:        "1ページ目の続きがある",
			entries:     entriesOf(5, 4, 3),
			skip:        0,
			perPage:     2,
			wantIDs:     []int{5, 4},
			wantHasNext: true,
		},
		{
			name:        "最後のページ",
			entries:     entriesOf(5, 4),
			skip:        0,
			perPage:     2,
			wantIDs:     []int{5, 4},
			wantHasNext: false,
		},
		{
			name:        "2ページ目",
			entries:     entriesOf(5, 4, 3, 2, 1),
			skip:        2,
			perPage:     2,
			wantIDs:     []int{3, 2},
			wantHasNext: true,
		},
		{
			// 削除で連番に穴が空いていても件数でずれないことの確認。
			name:        "穴が空いていても件数で切る",
			entries:     entriesOf(9, 7, 3, 2),
			skip:        2,
			perPage:     2,
			wantIDs:     []int{3, 2},
			wantHasNext: false,
		},
		{
			name:        "行き過ぎたページは空",
			entries:     entriesOf(5, 4),
			skip:        20,
			perPage:     2,
			wantIDs:     []int{},
			wantHasNext: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, hasNext := statusesSlice(c.entries, c.skip, c.perPage)
			gotIDs := idsOf(got)
			if len(gotIDs) != len(c.wantIDs) {
				t.Fatalf("got %v, want %v", gotIDs, c.wantIDs)
			}
			for i := range gotIDs {
				if gotIDs[i] != c.wantIDs[i] {
					t.Fatalf("got %v, want %v", gotIDs, c.wantIDs)
				}
			}
			if hasNext != c.wantHasNext {
				t.Errorf("hasNext = %v, want %v", hasNext, c.wantHasNext)
			}
		})
	}
}

func TestStatusesPageRender(t *testing.T) {
	page := statusesPage{
		pageBase: pageBase{Title: "投稿", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Statuses: []statusesItem{{
			StatusID:  7,
			Content:   "<p>こんにちは</p>",
			Published: "2026-08-03T12:00:00Z",
		}},
		Page:     2,
		PrevPage: 1,
		NextPage: 3,
		HasPrev:  true,
		HasNext:  true,
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "statuses", page); err != nil {
		t.Fatalf("rendering statuses failed: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		"/u/nana/status/7",
		"/u/nana/status?page=1",
		"/u/nana/status?page=3",
		"こんにちは",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
}

// 一覧の /u/:user/status と個別の /u/:user/status/:id は同じ前置きを持つ。
// httprouter に両方登録できていることを確かめる。
func TestStatusRoutes(t *testing.T) {
	r := newRouter()
	for _, path := range []string{"/u/nana/status", "/u/nana/status/1"} {
		h, _, _ := r.Lookup(http.MethodGet, path)
		if h == nil {
			t.Errorf("GET %v has no handler", path)
		}
	}
}
