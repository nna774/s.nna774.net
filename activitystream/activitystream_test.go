package activitystream

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func loadFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %v: %v", name, err)
	}
	return b
}

func decodeFixture(t *testing.T, name string) *Object {
	t.Helper()
	o := &Object{}
	if err := json.Unmarshal(loadFixture(t, name), o); err != nil {
		t.Fatalf("decoding fixture %v: %v", name, err)
	}
	return o
}

// これがこのリポジトリで一番重要なテストである。
//
// Mastodon が送る Follow の object は文字列 URI だが、以前は object を
// *Object 決め打ちで受けていたため "cannot unmarshal string into
// baseObject" で decode に失敗し、inbox が 422 を返していた。つまり
// 誰もこのインスタンスをフォローできなかった。
func TestDecodeIncomingFollowWithStringObject(t *testing.T) {
	in := decodeFixture(t, "follow_incoming.json")

	if in.Type != FollowType {
		t.Errorf("Type = %q, want %q", in.Type, FollowType)
	}
	if got, want := in.Actor.ID(), "https://pawoo.net/users/kugayama"; got != want {
		t.Errorf("Actor.ID() = %q, want %q", got, want)
	}
	// 文字列 URI で来ていても ID() が取れなければならない。
	if got, want := in.Object.ID(), "https://s.nna774.net/u/nana"; got != want {
		t.Errorf("Object.ID() = %q, want %q", got, want)
	}
	// 文字列で来たので埋め込みオブジェクトは無い。
	if in.Object.Item() != nil {
		t.Error("Object.Item() should be nil for a string URI")
	}
}

// Accept の object には元の Follow が id と type を含んだまま入っていな
// ければならない。actor と object だけを詰め直したものは Mastodon に
// 拒否される。
func TestNewAcceptEmbedsOriginalActivity(t *testing.T) {
	follow := decodeFixture(t, "follow_incoming.json")
	accept := NewAccept(follow, "https://s.nna774.net/u/nana", "https://s.nna774.net/accept/1")

	b, err := json.Marshal(accept)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got struct {
		Type   string `json:"type"`
		Actor  string `json:"actor"`
		Object struct {
			ID     string `json:"id"`
			Type   string `json:"type"`
			Actor  string `json:"actor"`
			Object string `json:"object"`
		} `json:"object"`
	}
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("Unmarshal of marshaled Accept: %v\n%s", err, b)
	}
	if got.Type != AcceptType {
		t.Errorf("type = %q, want %q", got.Type, AcceptType)
	}
	if got.Actor != "https://s.nna774.net/u/nana" {
		t.Errorf("actor = %q", got.Actor)
	}
	if got.Object.ID != follow.ID {
		t.Errorf("object.id = %q, want %q", got.Object.ID, follow.ID)
	}
	if got.Object.Type != FollowType {
		t.Errorf("object.type = %q, want %q", got.Object.Type, FollowType)
	}
	// 元の Follow の actor / object もそのまま残っていること。
	if got.Object.Actor != "https://pawoo.net/users/kugayama" {
		t.Errorf("object.actor = %q", got.Object.Actor)
	}
	if got.Object.Object != "https://s.nna774.net/u/nana" {
		t.Errorf("object.object = %q", got.Object.Object)
	}
}

// Create に Note が埋め込まれている形。object が埋め込みオブジェクトの
// 側の経路。
func TestDecodeCreateWithEmbeddedNote(t *testing.T) {
	in := decodeFixture(t, "create_note.json")

	if in.Type != CreateType {
		t.Errorf("Type = %q, want %q", in.Type, CreateType)
	}
	note := in.Object.Item()
	if note == nil {
		t.Fatal("Object.Item() is nil, want the embedded Note")
	}
	if note.Type != NoteType {
		t.Errorf("note.Type = %q, want %q", note.Type, NoteType)
	}
	if got, want := note.Content, "はいさーい"; got != want {
		t.Errorf("note.Content = %q, want %q", got, want)
	}
	if got, want := note.AttributedTo.ID(), "https://actub.ub32.org/argrath"; got != want {
		t.Errorf("note.AttributedTo.ID() = %q, want %q", got, want)
	}
	if len(note.To) != 1 || note.To[0] != ToPublic {
		t.Errorf("note.To = %v, want [%v]", note.To, ToPublic)
	}
	// 埋め込みオブジェクトでも Ref.ID() は id を返す。
	if got, want := in.Object.ID(), "https://actub.ub32.org/argrath/123"; got != want {
		t.Errorf("Object.ID() = %q, want %q", got, want)
	}
}

// pawoo.net の実 outbox ページ。@context が配列とオブジェクトの混在で、
// ostatus:/toot: 拡張を含む。現実の Mastodon 出力が読めることの確認。
func TestDecodeMastodonOrderedCollectionPage(t *testing.T) {
	page := decodeFixture(t, "outbox_page_mastodon.json")

	if page.Type != OrderedCollectionPageType {
		t.Errorf("Type = %q, want %q", page.Type, OrderedCollectionPageType)
	}
	if page.Next == "" || page.Prev == "" {
		t.Errorf("next/prev should be present: next=%q prev=%q", page.Next, page.Prev)
	}
	if len(page.OrderedItems) == 0 {
		t.Fatal("orderedItems is empty")
	}
	// @context が配列 + オブジェクトの混在でも落ちないこと。
	if page.Context == nil {
		t.Error("@context was dropped")
	}
	for i, item := range page.OrderedItems {
		if item.Type == "" {
			t.Errorf("orderedItems[%d] has no type", i)
		}
		if item.Actor.ID() == "" {
			t.Errorf("orderedItems[%d] has no actor", i)
		}
	}
}

// 平坦化前は MarshalJSON の default 分岐が Person と OrderedCollection の
// 中身を丸ごと落としていた。actor から inbox/outbox/publicKey が、outbox
// から totalItems/first/last が消えていた。
func TestMarshalDoesNotDropTypeSpecificFields(t *testing.T) {
	t.Run("Person", func(t *testing.T) {
		actor := NewUserResource(
			"https://s.nna774.net/u/nana", "久我山菜々",
			"https://nna774.net/img/a.jpg", "image/jpeg", "nana",
			"https://s.nna774.net/u/nana/inbox", "https://s.nna774.net/u/nana/outbox",
			"https://s.nna774.net/u/nana/followers", "https://s.nna774.net/u/nana/following",
			"summary", "https://s.nna774.net/u/nana#main-key", "PEM",
		)
		got := marshalToMap(t, actor)
		for _, k := range []string{"@context", "id", "type", "url", "name", "icon", "preferredUsername", "inbox", "outbox", "followers", "following", "summary", "publicKey"} {
			if _, ok := got[k]; !ok {
				t.Errorf("actor JSON is missing %q", k)
			}
		}
		pk, _ := got["publicKey"].(map[string]interface{})
		if pk["publicKeyPem"] != "PEM" {
			t.Errorf("publicKey.publicKeyPem = %v", pk["publicKeyPem"])
		}
	})

	t.Run("OrderedCollection", func(t *testing.T) {
		got := marshalToMap(t, NewOrderedCollection("https://s.nna774.net/u/nana/outbox", 12, "first", "last"))
		for _, k := range []string{"@context", "id", "type", "totalItems", "first", "last"} {
			if _, ok := got[k]; !ok {
				t.Errorf("outbox JSON is missing %q", k)
			}
		}
		if got["totalItems"] != float64(12) {
			t.Errorf("totalItems = %v, want 12", got["totalItems"])
		}
	})

	t.Run("OrderedCollectionPage", func(t *testing.T) {
		note := NewNote("https://s.nna774.net/u/nana/status/1", "2026-07-28T00:00:00Z", "", "yo", "https://s.nna774.net/u/nana", []string{ToPublic}, nil, nil)
		got := marshalToMap(t, NewOrderedCollectionPage("id", "partOf", "next", "prev", []*Object{note}))
		for _, k := range []string{"@context", "id", "type", "partOf", "next", "prev", "orderedItems"} {
			if _, ok := got[k]; !ok {
				t.Errorf("page JSON is missing %q", k)
			}
		}
		items, _ := got["orderedItems"].([]interface{})
		if len(items) != 1 {
			t.Fatalf("orderedItems has %d entries, want 1", len(items))
		}
	})
}

// 本番 DynamoDB に 2023 年から入っている実データ。モデルを変えるたびに、
// 保存済みのものが読めなくなっていないかを確認する必要がある。読めなく
// なると outbox が壊れる。
func TestStoredOutboxItemRoundTrips(t *testing.T) {
	raw := loadFixture(t, "outbox_create_stored.json")
	create := decodeFixture(t, "outbox_create_stored.json")

	if create.Type != CreateType {
		t.Errorf("Type = %q, want %q", create.Type, CreateType)
	}
	if got, want := create.Actor.ID(), "https://s.nna774.net/u/nana"; got != want {
		t.Errorf("Actor.ID() = %q, want %q", got, want)
	}
	note := create.Object.Item()
	if note == nil {
		t.Fatal("embedded Note was lost")
	}
	if got, want := note.ID, "https://s.nna774.net/u/nana/status/1089"; got != want {
		t.Errorf("note.ID = %q, want %q", got, want)
	}
	if len(note.Tag) != 1 || note.Tag[0].Type != MentionType {
		t.Errorf("note.Tag = %+v, want one Mention", note.Tag)
	}
	if got, want := note.Tag[0].Href, "https://pawoo.net/users/kugayama"; got != want {
		t.Errorf("note.Tag[0].Href = %q, want %q", got, want)
	}
	if len(note.Cc) != 2 {
		t.Errorf("note.Cc = %v, want 2 entries", note.Cc)
	}

	// 元の JSON と再エンコード結果が、キー順の違いを除いて等価であること。
	// フィールドが落ちていれば差が出る。
	var want, got interface{}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	reencoded, err := json.Marshal(create)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := json.Unmarshal(reencoded, &got); err != nil {
		t.Fatalf("Unmarshal reencoded: %v", err)
	}
	if !jsonEqual(want, got) {
		t.Errorf("round trip changed the document\n--- stored ---\n%s\n--- reencoded ---\n%s", raw, reencoded)
	}
}

func jsonEqual(a, b interface{}) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	// json.Marshal は map のキーをソートするため、キー順の違いは吸収される。
	return err1 == nil && err2 == nil && string(ab) == string(bb)
}

// totalItems が 0 のときに omitempty で消えてはならない。空の outbox でも
// totalItems: 0 を出す必要がある。
func TestZeroTotalItemsIsKept(t *testing.T) {
	got := marshalToMap(t, NewOrderedCollection("id", 0, "", ""))
	v, ok := got["totalItems"]
	if !ok {
		t.Fatal("totalItems was dropped when it is 0")
	}
	if v != float64(0) {
		t.Errorf("totalItems = %v, want 0", v)
	}
}

func TestStringsAcceptsSingleValueAndArray(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want []string
	}{
		{"array", `{"to":["a","b"]}`, []string{"a", "b"}},
		{"single string", `{"to":"a"}`, []string{"a"}},
		{"null", `{"to":null}`, nil},
		{"absent", `{}`, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := &Object{}
			if err := json.Unmarshal([]byte(tt.in), o); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if len(o.To) != len(tt.want) {
				t.Fatalf("To = %v, want %v", o.To, tt.want)
			}
			for i := range tt.want {
				if o.To[i] != tt.want[i] {
					t.Errorf("To[%d] = %q, want %q", i, o.To[i], tt.want[i])
				}
			}
		})
	}
}

func TestObjectsAcceptsSingleValueAndArray(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   string
		want int
	}{
		{"array", `{"tag":[{"type":"Mention"},{"type":"Hashtag"}]}`, 2},
		{"single object", `{"tag":{"type":"Mention"}}`, 1},
		{"null", `{"tag":null}`, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			o := &Object{}
			if err := json.Unmarshal([]byte(tt.in), o); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if len(o.Tag) != tt.want {
				t.Errorf("len(Tag) = %d, want %d", len(o.Tag), tt.want)
			}
		})
	}
}

func TestRefRoundTrip(t *testing.T) {
	t.Run("URI stays a string", func(t *testing.T) {
		b, err := json.Marshal(&Object{Object: URIRef("https://example.com/1")})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if got, want := string(b), `{"object":"https://example.com/1"}`; got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
	t.Run("object stays an object", func(t *testing.T) {
		b, err := json.Marshal(&Object{Object: ObjectRef(&Object{ID: "x", Type: NoteType})})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if got, want := string(b), `{"object":{"id":"x","type":"Note"}}`; got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
	t.Run("nil ref is omitted", func(t *testing.T) {
		b, err := json.Marshal(&Object{})
		if err != nil {
			t.Fatalf("Marshal: %v", err)
		}
		if got, want := string(b), `{}`; got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
}

func TestRefIDOnNil(t *testing.T) {
	var r *Ref
	if got := r.ID(); got != "" {
		t.Errorf("(*Ref)(nil).ID() = %q, want empty", got)
	}
	if got := r.Item(); got != nil {
		t.Errorf("(*Ref)(nil).Item() = %v, want nil", got)
	}
}

func TestInboxURIPrefersSharedInbox(t *testing.T) {
	for _, tt := range []struct {
		name string
		in   *Object
		want string
	}{
		{"sharedInbox wins", &Object{Inbox: "i", Endpoints: &Endpoints{SharedInbox: "s"}}, "s"},
		{"falls back to inbox", &Object{Inbox: "i", Endpoints: &Endpoints{}}, "i"},
		{"no endpoints", &Object{Inbox: "i"}, "i"},
		{"nothing", &Object{}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.in.InboxURI(); got != tt.want {
				t.Errorf("InboxURI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func marshalToMap(t *testing.T, v interface{}) map[string]interface{} {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	return m
}
