package activitystream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
)

const (
	// ActivityStreamsContentType はリモートに actor を取りに行くときの
	// Accept ヘッダに使う。
	ActivityStreamsContentType = `application/ld+json; profile="https://www.w3.org/ns/activitystreams"`
	// ContentType は自分が ActivityPub の JSON を返すときの Content-Type。
	ContentType = "application/activity+json"

	ToPublic = "https://www.w3.org/ns/activitystreams#Public"

	ContextActivityStreams = "https://www.w3.org/ns/activitystreams"
	ContextSecurityV1      = "https://w3id.org/security/v1"
)

const (
	AcceptType                = "Accept"
	AnnounceType              = "Announce"
	CreateType                = "Create"
	DeleteType                = "Delete"
	FollowType                = "Follow"
	ImageType                 = "Image"
	LikeType                  = "Like"
	MentionType               = "Mention"
	NoteType                  = "Note"
	OrderedCollectionType     = "OrderedCollection"
	OrderedCollectionPageType = "OrderedCollectionPage"
	PersonType                = "Person"
	// ServiceType は bot 等の自動投稿アカウントを表す。Mastodon 等はこれを
	// 見て自動化されたアカウントであることを表示する。
	ServiceType = "Service"
	// PropertyValueType は ActivityStreams ではなく schema.org 由来だが、
	// Mastodon がプロフィールの追加情報の表現に使っているため合わせる。
	PropertyValueType = "PropertyValue"
	RejectType        = "Reject"
	UndoType          = "Undo"
	UpdateType        = "Update"
)

// Ref は ActivityPub の「文字列 URI または埋め込みオブジェクト」を表す。
// object / actor / attributedTo / inReplyTo などはどちらの形でも来るため、
// 片方しか受けられないと相互運用性が無い。実際、Mastodon が送る Follow の
// object は文字列 URI である。
type Ref struct {
	URI    string
	Object *Object
}

func URIRef(uri string) *Ref {
	if uri == "" {
		return nil
	}
	return &Ref{URI: uri}
}

func ObjectRef(o *Object) *Ref {
	if o == nil {
		return nil
	}
	return &Ref{URI: o.ID, Object: o}
}

// ID はどちらの形で来ていても対象の id を返す。
func (r *Ref) ID() string {
	if r == nil {
		return ""
	}
	if r.Object != nil && r.Object.ID != "" {
		return r.Object.ID
	}
	return r.URI
}

// Item は埋め込みオブジェクトを返す。文字列 URI だった場合は nil。
func (r *Ref) Item() *Object {
	if r == nil {
		return nil
	}
	return r.Object
}

func (r *Ref) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '"' {
		return json.Unmarshal(b, &r.URI)
	}
	var obj Object
	if err := json.Unmarshal(b, &obj); err != nil {
		return fmt.Errorf("ref is neither a URI string nor an object: %w", err)
	}
	r.Object = &obj
	r.URI = obj.ID
	return nil
}

func (r Ref) MarshalJSON() ([]byte, error) {
	if r.Object != nil {
		return json.Marshal(r.Object)
	}
	return json.Marshal(r.URI)
}

// Strings は単一の文字列と配列の両方を受ける。to / cc は実装によって
// どちらの形でも来る。
type Strings []string

func (s *Strings) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '[' {
		return json.Unmarshal(b, (*[]string)(s))
	}
	var one string
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*s = Strings{one}
	return nil
}

// Objects は単一オブジェクトと配列の両方を受ける。tag が該当する。
type Objects []*Object

func (os *Objects) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	if b[0] == '[' {
		return json.Unmarshal(b, (*[]*Object)(os))
	}
	var one Object
	if err := json.Unmarshal(b, &one); err != nil {
		return err
	}
	*os = Objects{&one}
	return nil
}

// Object は ActivityStreams のオブジェクトと Activity を1つの構造体で
// 表す。以前は型ごとの値を interface{} に持たせて MarshalJSON で分岐して
// いたが、型を足すたびに Unmarshal/Marshal/アクセサ/型定数の4箇所を
// 揃える必要があり、1つ漏らすとフィールドが黙って消えた。実際に Person と
// OrderedCollection で消えていた。平坦な構造体にすれば構造的に起こらない。
type Object struct {
	Context interface{} `json:"@context,omitempty"`
	ID      string      `json:"id,omitempty"`
	Type    string      `json:"type,omitempty"`

	URL       string `json:"url,omitempty"`
	Name      string `json:"name,omitempty"`
	Summary   string `json:"summary,omitempty"`
	Content   string `json:"content,omitempty"`
	MediaType string `json:"mediaType,omitempty"`
	Published string `json:"published,omitempty"`
	Sensitive *bool  `json:"sensitive,omitempty"`

	AttributedTo *Ref `json:"attributedTo,omitempty"`
	Actor        *Ref `json:"actor,omitempty"`
	Object       *Ref `json:"object,omitempty"`
	Target       *Ref `json:"target,omitempty"`
	InReplyTo    *Ref `json:"inReplyTo,omitempty"`

	To Strings `json:"to,omitempty"`
	Cc Strings `json:"cc,omitempty"`

	Icon       *Object `json:"icon,omitempty"`
	Tag        Objects `json:"tag,omitempty"`
	Attachment Objects `json:"attachment,omitempty"`
	// Value は ActivityStreams には無いが、PropertyValue が持っている。
	Value string `json:"value,omitempty"`
	// Href は ActivityStreams には無いが、Mastodon の tag が持っている。
	Href string `json:"href,omitempty"`

	PreferredUsername string     `json:"preferredUsername,omitempty"`
	Inbox             string     `json:"inbox,omitempty"`
	Outbox            string     `json:"outbox,omitempty"`
	Followers         string     `json:"followers,omitempty"`
	Following         string     `json:"following,omitempty"`
	Liked             string     `json:"liked,omitempty"`
	Endpoints         *Endpoints `json:"endpoints,omitempty"`
	PublicKey         *PublicKey `json:"publicKey,omitempty"`

	TotalItems *int   `json:"totalItems,omitempty"`
	First      string `json:"first,omitempty"`
	Last       string `json:"last,omitempty"`
	Next       string `json:"next,omitempty"`
	Prev       string `json:"prev,omitempty"`
	PartOf     string `json:"partOf,omitempty"`
	// OrderedItems は要素がオブジェクトのことも裸の URI 文字列のことも
	// ある。outbox は前者、followers / following は後者。
	OrderedItems []*Ref `json:"orderedItems,omitempty"`
}

type Endpoints struct {
	SharedInbox string `json:"sharedInbox,omitempty"`
}

type PublicKey struct {
	ID           string `json:"id"`
	Owner        string `json:"owner"`
	PublicKeyPem string `json:"publicKeyPem"`
}

// InboxURI は配信先を返す。sharedInbox があればそちらを優先する。
// 同一インスタンスの複数フォロワーへの配信を1回にまとめられる。
func (o *Object) InboxURI() string {
	if o == nil {
		return ""
	}
	if o.Endpoints != nil && o.Endpoints.SharedInbox != "" {
		return o.Endpoints.SharedInbox
	}
	return o.Inbox
}

// FetchActorInfo はリモートの actor を取得する。表示名やアイコンも
// 使いたいので Object をそのまま返す。
func FetchActorInfo(ctx context.Context, actor string) (*Object, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, actor, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("accept", ActivityStreamsContentType)
	resp, err := http.DefaultClient.Do(req) // TODO: maybe follow redurect
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetchActorInfo failed. actor %v returned %v", actor, resp.Status)
	}
	var o Object
	if err := json.NewDecoder(resp.Body).Decode(&o); err != nil {
		return nil, fmt.Errorf("fetchActorInfo: decoding %v failed: %w", actor, err)
	}
	return &o, nil
}

// propertyValueContext は PropertyValue と value の語彙を定義する。
// Mastodon は type の名前だけを見ていて @context を読まないが、JSON-LD と
// して読む実装のために定義しておく。
var propertyValueContext = map[string]string{
	"schema":        "http://schema.org#",
	"PropertyValue": "schema:PropertyValue",
	"value":         "schema:value",
}

// NewPropertyValue はプロフィールの追加情報を1項目作る。value が http(s)
// の URL のときは rel="me" 付きのリンクにする。Mastodon はそのリンク先を
// 取りに行き、actor へ戻る rel="me" があれば検証済みとして表示する。
func NewPropertyValue(name string, value string) *Object {
	v := html.EscapeString(value)
	if u, err := url.Parse(value); err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" {
		v = `<a href="` + v + `" rel="me nofollow noopener" target="_blank">` + v + `</a>`
	}
	return &Object{
		Type:  PropertyValueType,
		Name:  name,
		Value: v,
	}
}

func NewUserResource(id string, actorType string, name string, iconURI string, iconMediaType string, preferredUsername string, inbox string, outbox string, followers string, following string, summary string, keyID string, publicKey string, attachment Objects) *Object {
	return &Object{
		Context:    []interface{}{ContextActivityStreams, ContextSecurityV1, propertyValueContext},
		ID:         id,
		Type:       actorType,
		URL:        id,
		Name:       name,
		Attachment: attachment,
		Icon: &Object{
			Type:      ImageType,
			MediaType: iconMediaType,
			URL:       iconURI,
		},
		PreferredUsername: preferredUsername,
		Inbox:             inbox,
		Outbox:            outbox,
		Followers:         followers,
		Following:         following,
		Summary:           summary,
		PublicKey: &PublicKey{
			ID:           keyID,
			Owner:        id,
			PublicKeyPem: publicKey,
		},
	}
}

// NewAccept は受け取った Activity をそのまま object に入れた Accept を作る。
// Mastodon は Accept の object に元の Follow の id と type が入っていることを
// 要求するため、actor と object だけを詰め直したものでは拒否される。
func NewAccept(activity *Object, actorID string, acceptID string) *Object {
	return &Object{
		Context: ContextActivityStreams,
		ID:      acceptID,
		Type:    AcceptType,
		Actor:   URIRef(actorID),
		Object:  ObjectRef(activity),
	}
}

func NewReject(activity *Object, actorID string, rejectID string) *Object {
	return &Object{
		Context: ContextActivityStreams,
		ID:      rejectID,
		Type:    RejectType,
		Actor:   URIRef(actorID),
		Object:  ObjectRef(activity),
	}
}

func NewFollow(followID string, actorID string, targetID string) *Object {
	return &Object{
		Context: ContextActivityStreams,
		ID:      followID,
		Type:    FollowType,
		Actor:   URIRef(actorID),
		Object:  URIRef(targetID),
	}
}

func NewUndo(activity *Object, actorID string, undoID string) *Object {
	return &Object{
		Context: ContextActivityStreams,
		ID:      undoID,
		Type:    UndoType,
		Actor:   URIRef(actorID),
		Object:  ObjectRef(activity),
	}
}

func NewNote(noteID string, published string, name string, content string, attributedTo string, to []string, cc []string, tag []*Object) *Object {
	return &Object{
		Context:      ContextActivityStreams,
		ID:           noteID,
		Type:         NoteType,
		URL:          noteID,
		Published:    published,
		Name:         name,
		Content:      content,
		AttributedTo: URIRef(attributedTo),
		To:           to,
		Cc:           cc,
		Tag:          tag,
	}
}

func NewCreate(createID string, actor string, to []string, cc []string, obj *Object) *Object {
	return &Object{
		Context: ContextActivityStreams,
		ID:      createID,
		Type:    CreateType,
		To:      to,
		Cc:      cc,
		Actor:   URIRef(actor),
		Object:  ObjectRef(obj),
	}
}

func NewLike(likeID string, actorID string, objectID string) *Object {
	return &Object{
		Context: ContextActivityStreams,
		ID:      likeID,
		Type:    LikeType,
		Actor:   URIRef(actorID),
		Object:  URIRef(objectID),
	}
}

func NewAnnounce(announceID string, actorID string, objectID string, to []string, cc []string) *Object {
	return &Object{
		Context: ContextActivityStreams,
		ID:      announceID,
		Type:    AnnounceType,
		Actor:   URIRef(actorID),
		Object:  URIRef(objectID),
		To:      to,
		Cc:      cc,
	}
}

func NewDelete(deleteID string, actor string, to []string, objectID string) *Object {
	return &Object{
		Context: ContextActivityStreams,
		ID:      deleteID,
		Type:    DeleteType,
		To:      to,
		Actor:   URIRef(actor),
		Object:  URIRef(objectID),
	}
}

func NewTag(tagType string, name string, href string) *Object {
	return &Object{
		Type: tagType,
		Name: name,
		Href: href,
	}
}

// NewMention は name に @user@host 形式の表記を要求する。以前は
// pawoo.net の特定アカウントがハードコードされていた。
func NewMention(name string, actorURI string) *Object {
	return NewTag(MentionType, name, actorURI)
}

func NewOrderedCollection(id string, totalItems int, first string, last string) *Object {
	return &Object{
		Context:    ContextActivityStreams,
		ID:         id,
		Type:       OrderedCollectionType,
		TotalItems: &totalItems,
		First:      first,
		Last:       last,
	}
}

// NewOrderedCollectionOfIDs は URI の並びをそのまま orderedItems に持つ
// OrderedCollection を作る。followers / following はこの形で返す。
func NewOrderedCollectionOfIDs(id string, ids []string) *Object {
	total := len(ids)
	items := make([]*Ref, 0, len(ids))
	for _, s := range ids {
		items = append(items, URIRef(s))
	}
	return &Object{
		Context:      ContextActivityStreams,
		ID:           id,
		Type:         OrderedCollectionType,
		TotalItems:   &total,
		OrderedItems: items,
	}
}

func NewOrderedCollectionPage(id string, partOf string, next string, prev string, orderedItems []*Object) *Object {
	items := make([]*Ref, 0, len(orderedItems))
	for _, o := range orderedItems {
		items = append(items, ObjectRef(o))
	}
	return &Object{
		Context:      ContextActivityStreams,
		ID:           id,
		Type:         OrderedCollectionPageType,
		PartOf:       partOf,
		Next:         next,
		Prev:         prev,
		OrderedItems: items,
	}
}
