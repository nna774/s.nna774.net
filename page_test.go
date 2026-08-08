package main

import (
	"bytes"
	"net/http"
	"strings"
	"testing"

	"github.com/nna774/s.nna774.net/activitystream"
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

func TestPaginateEntries(t *testing.T) {
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
			got, hasNext := paginate(c.entries, c.skip, c.perPage)
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

// 絞り込みリンクは、選択中のものだけリンクにならない (アクティブ表示)。
// 選択されていないと通常のリンクが出る。
func TestStatusesPageRendersFilterLinks(t *testing.T) {
	page := statusesPage{
		pageBase: pageBase{Title: "投稿", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Filter:   statusFilterBoosts,
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "statuses", page); err != nil {
		t.Fatalf("rendering statuses failed: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		`<a href="/u/nana/status">すべて</a>`,
		`<b>ブーストのみ</b>`,
		`<a href="/u/nana/status?filter=media">メディアのみ</a>`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, `<a href="/u/nana/status?filter=boosts">`) {
		t.Errorf("active filter should not be a link:\n%s", html)
	}
}

// ページャは選択中の絞り込みを引き継ぐ。
func TestStatusesPagePagerCarriesFilter(t *testing.T) {
	page := statusesPage{
		pageBase: pageBase{Title: "投稿", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Filter:   statusFilterMedia,
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
		`href="/u/nana/status?page=1&filter=media"`,
		`href="/u/nana/status?page=3&filter=media"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q:\n%s", want, html)
		}
	}
}

// /u/:user/status にも自分のブーストが「著者名 の投稿をブースト」の形で
// 混ざり、日時は元投稿ではなく Announce 自身にリンクされることを確かめる。
func TestStatusesPageRendersBoosts(t *testing.T) {
	page := statusesPage{
		pageBase: pageBase{Title: "投稿", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Statuses: []statusesItem{
			{StatusID: 7, Content: "<p>自分の投稿</p>", Published: "2026-08-03T12:00:00Z"},
			{
				Content:     "<p>他人の投稿</p>",
				Published:   "2026-08-03T13:00:00Z",
				Boosted:     true,
				AuthorName:  "someone",
				AuthorURI:   "https://example.com/users/someone",
				AnnounceURI: "https://s.nna774.net/announce/123",
			},
		},
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "statuses", page); err != nil {
		t.Fatalf("rendering statuses failed: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		"自分の投稿",
		"他人の投稿",
		`href="/remote?actor=https%3a%2f%2fexample.com%2fusers%2fsomeone">someone</a> の投稿をブースト`,
		`href="https://s.nna774.net/announce/123"`,
		`/u/nana/status/7`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
}

func TestNoteAttachments(t *testing.T) {
	note := &activitystream.Object{
		Attachment: activitystream.Objects{
			{URL: "https://example.com/a.png", MediaType: "image/png", Name: "猫"},
			{URL: "https://example.com/a.mp4", MediaType: "video/mp4"},
			{URL: "https://example.com/a.pdf", MediaType: "application/pdf"},
			// url を持たない attachment (プロフィール項目などに紛れ込んだ場合)
			// は無視する。
			{Name: "url無し"},
		},
	}
	got := noteAttachments(note)
	want := []attachmentItem{
		{URL: "https://example.com/a.png", Name: "猫", Kind: "image"},
		{URL: "https://example.com/a.mp4", Kind: "video"},
		{URL: "https://example.com/a.pdf", Kind: "other"},
	}
	if len(got) != len(want) {
		t.Fatalf("noteAttachments() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("noteAttachments()[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

// Gyazo 由来の画像添付は、サムネイルをクリックすると Gyazo の画像ページへ
// リンクしていることを HTML 出力で確認する。
func TestStatusesPageRenderLinksGyazoThumbnailToImagePage(t *testing.T) {
	page := statusesPage{
		pageBase: pageBase{Title: "投稿", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Statuses: []statusesItem{{
			StatusID:  7,
			Content:   "<p>こんにちは</p>",
			Published: "2026-08-03T12:00:00Z",
			Attachments: []attachmentItem{
				{
					URL:     "https://i.gyazo.com/1234567890abcdef1234567890abcdef.png",
					Kind:    "image",
					PageURL: "https://gyazo.com/1234567890abcdef1234567890abcdef",
				},
			},
		}},
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "statuses", page); err != nil {
		t.Fatalf("rendering statuses failed: %v", err)
	}
	html := buf.String()
	if !strings.Contains(html, `<a href="https://gyazo.com/1234567890abcdef1234567890abcdef" target="_blank" rel="noopener noreferrer">`) {
		t.Errorf("rendered page does not link the thumbnail to the gyazo image page: %s", html)
	}
}

func TestNoteAttachmentsEmpty(t *testing.T) {
	if got := noteAttachments(&activitystream.Object{}); got != nil {
		t.Errorf("noteAttachments() = %#v, want nil", got)
	}
}

// Gyazo 由来の添付はサムネイル表示が小さいため、タイムラインからクリック
// で原寸の画像ページへ飛べるように PageURL を持たせる。
func TestNoteAttachmentsSetsGyazoPageURL(t *testing.T) {
	note := &activitystream.Object{
		Attachment: activitystream.Objects{
			{URL: "https://i.gyazo.com/1234567890abcdef1234567890abcdef.png", MediaType: "image/png"},
		},
	}
	got := noteAttachments(note)
	want := []attachmentItem{
		{
			URL:     "https://i.gyazo.com/1234567890abcdef1234567890abcdef.png",
			Kind:    "image",
			PageURL: "https://gyazo.com/1234567890abcdef1234567890abcdef",
		},
	}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("noteAttachments() = %#v, want %#v", got, want)
	}
}

func TestTimelinePageRenderWithAttachment(t *testing.T) {
	page := timelinePage{
		pageBase: pageBase{Title: "タイムライン", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Items: []timelineItem{{
			AuthorName: "someone",
			AuthorURI:  "https://example.com/users/someone",
			Content:    "<p>写真</p>",
			Attachments: []attachmentItem{
				{URL: "https://example.com/a.png", Name: "猫", Kind: "image"},
				{URL: "https://example.com/a.mp4", Kind: "video"},
				{URL: "https://example.com/a.pdf", Kind: "other"},
			},
			Published: "2026-08-03T12:00:00Z",
		}},
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "timeline", page); err != nil {
		t.Fatalf("rendering timeline failed: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		`<img src="https://example.com/a.png" alt="猫"`,
		`<video src="https://example.com/a.mp4" controls>`,
		`<a href="https://example.com/a.pdf"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
}

func TestTimelinePagerRender(t *testing.T) {
	page := timelinePage{
		pageBase: pageBase{Title: "タイムライン", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Items: []timelineItem{{
			AuthorName: "someone",
			AuthorURI:  "https://example.com/users/someone",
			Content:    "<p>古い投稿</p>",
			Published:  "2026-08-01T12:00:00Z",
		}},
		Page:     2,
		PrevPage: 1,
		NextPage: 3,
		HasPrev:  true,
		HasNext:  true,
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "timeline", page); err != nil {
		t.Fatalf("rendering timeline failed: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		"/timeline?page=1",
		"/timeline?page=3",
		"古い投稿",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
}

// プロフィールに自分のブーストが「著者名 の投稿をブースト」の形で出ること、
// 自分の投稿には出ないことを確かめる。
func TestProfilePageRendersBoosts(t *testing.T) {
	page := profilePage{
		pageBase: pageBase{Title: "prof", SiteName: "nana", LocalPart: "nana", Handle: "@nana"},
		Statuses: []profileStatusItem{
			{Content: "<p>自分の投稿</p>", URL: "https://s.nna774.net/u/nana/status/1", Published: "2026-08-03T12:00:00Z"},
			{
				Content:     "<p>他人の投稿</p>",
				URL:         "https://example.com/status/1",
				Published:   "2026-08-03T13:00:00Z",
				Boosted:     true,
				AuthorName:  "someone",
				AuthorURI:   "https://example.com/users/someone",
				AnnounceURI: "https://s.nna774.net/announce/123",
			},
		},
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "profile", page); err != nil {
		t.Fatalf("rendering profile failed: %v", err)
	}
	html := buf.String()
	if strings.Count(html, `<div class="boosted">`) != 1 {
		t.Errorf("want exactly one boosted marker, got:\n%s", html)
	}
	for _, want := range []string{
		"自分の投稿",
		"他人の投稿",
		`href="/remote?actor=https%3a%2f%2fexample.com%2fusers%2fsomeone">someone</a> の投稿をブースト`,
		// ブーストの日時は元投稿ではなく Announce 自身にリンクする。
		`href="https://s.nna774.net/announce/123"`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q", want)
		}
	}
}

// announce ページは認証されているときだけいいね・取り消しリンクを出す。
func TestAnnouncePageRendersActionsWhenAuthed(t *testing.T) {
	page := announcePage{
		pageBase:    pageBase{Title: "boost", SiteName: "nana", LocalPart: "nana", Handle: "@nana", Authed: true},
		AnnounceURI: "https://s.nna774.net/announce/123",
		Name:        "someone",
		AuthorURI:   "https://example.com/users/someone",
		Content:     "<p>ブーストされた投稿</p>",
		Published:   "2026-08-03T12:00:00Z",
		ObjectURI:   "https://example.com/status/1",
		Liked:       false,
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "announce", page); err != nil {
		t.Fatalf("rendering announce failed: %v", err)
	}
	html := buf.String()
	for _, want := range []string{
		`href="https://s.nna774.net/announce/123"`,
		`likeStatus(event, 'https:\/\/example.com\/status\/1', 'https:\/\/example.com\/users\/someone', 'nana')`,
		`cancelBoostAndLeave(event, 'https:\/\/example.com\/status\/1', 'nana')`,
	} {
		if !strings.Contains(html, want) {
			t.Errorf("rendered page does not contain %q:\n%s", want, html)
		}
	}
}

// 未認証では、いいね・取り消しのリンクを出さない。
func TestAnnouncePageHidesActionsWhenNotAuthed(t *testing.T) {
	page := announcePage{
		pageBase:    pageBase{Title: "boost", SiteName: "nana", LocalPart: "nana", Handle: "@nana", Authed: false},
		AnnounceURI: "https://s.nna774.net/announce/123",
		Name:        "someone",
		AuthorURI:   "https://example.com/users/someone",
		Content:     "<p>ブーストされた投稿</p>",
		Published:   "2026-08-03T12:00:00Z",
		ObjectURI:   "https://example.com/status/1",
	}
	buf := &bytes.Buffer{}
	if err := web.Render(buf, "announce", page); err != nil {
		t.Fatalf("rendering announce failed: %v", err)
	}
	html := buf.String()
	// layout.html の <script> は likeStatus/cancelBoostAndLeave の関数定義を
	// 常に含むため、呼び出し ("event, " 付き) の有無で判定する。
	for _, notWant := range []string{"likeStatus(event,", "cancelBoostAndLeave(event,"} {
		if strings.Contains(html, notWant) {
			t.Errorf("rendered page unexpectedly contains %q:\n%s", notWant, html)
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
