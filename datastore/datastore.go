package datastore

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/guregu/dynamo/v2"
	"github.com/nna774/s.nna774.net/activitystream"
)

var (
	// ErrNotFound is key not found err
	ErrNotFound = dynamo.ErrNotFound
)

type Order bool

const (
	Asc  Order = true
	Desc Order = false

	Inf int = math.MaxInt
)

type Client interface {
	Put(ctx context.Context, name string, id int, object interface{}) error
	GetObject(ctx context.Context, name string, id int) (*activitystream.Object, error)
	TakeObject(ctx context.Context, name string, base int, cnt int, order Order) ([]*activitystream.Object, error)
	// TakeEntries は TakeObject と同じだが連番も返す。取得したものを
	// あとで削除・更新したい場合はこちらを使う。
	TakeEntries(ctx context.Context, name string, base int, cnt int, order Order) ([]Entry, error)
	DeleteObject(ctx context.Context, name string, id int) error

	Inc(ctx context.Context, key string) (int, error)
	// Top returns count of the key. if key does not exist, return (0, ErrNotFound)
	Top(ctx context.Context, key string) (int, error)

	// KV は pk/sk がどちらも文字列のテーブルを使う。actor URI や keyId を
	// そのままキーにできる。
	PutKV(ctx context.Context, item *KVItem) error
	GetKV(ctx context.Context, pk, sk string) (*KVItem, error)
	QueryKV(ctx context.Context, pk string) ([]*KVItem, error)
	DeleteKV(ctx context.Context, pk, sk string) error
	CountKV(ctx context.Context, pk string) (int, error)
}

// KV のパーティション。
const (
	KVFollowers = "followers"
	KVFollowing = "following"
	KVActorKey  = "actorkey"
	KVSeen      = "seen"
	KVLikes     = "likes"
	// KVMyLikes / KVMyBoosts は自分が他人の投稿にいいね・ブーストした記録。
	// SK は対象投稿の URI。取り消し (Undo) のときに元の Activity の id と
	// 配信先を引き直さずに済ませるために持つ。KVLikes (自分の投稿に付いた
	// いいね) とは向きが逆なので分けてある。
	KVMyLikes  = "mylikes"
	KVMyBoosts = "myboosts"
	// KVMyBoostByID は KVMyBoosts の逆引き。KVMyBoosts は対象投稿の URI を
	// キーにしているが、Announce 自身の URI (/announce/:id) から辿るには
	// Announce.ID をキーにした索引が要る。
	KVMyBoostByID = "myboostbyid"
	// KVCursor は「どこまで読んだか」を持つ。通知の未読判定に使う。
	KVCursor = "cursor"
	// KVActorInfo はフォロー関係にない相手の表示名とアイコンのキャッシュ。
	// いいねやブーストは誰からでも来る。
	KVActorInfo = "actorinfo"
)

// KVItem は KV テーブルの1項目。用途ごとに使うフィールドが異なるので
// すべて omitempty にしてある。
type KVItem struct {
	PK string `dynamo:"pk"`
	SK string `dynamo:"sk"`

	// フォロワー / フォロー中
	Inbox       string `dynamo:"inbox,omitempty"`
	SharedInbox string `dynamo:"sharedInbox,omitempty"`
	Name        string `dynamo:"name,omitempty"`
	IconURL     string `dynamo:"iconURL,omitempty"`
	// ActivityID は Undo を受けたときに突き合わせる元の Follow の id。
	ActivityID string `dynamo:"activityID,omitempty"`
	// State は following で使う。pending か accepted。
	State string `dynamo:"state,omitempty"`
	// At は登録時刻 (RFC3339)。
	At string `dynamo:"at,omitempty"`

	// 公開鍵キャッシュ
	PublicKeyPem string `dynamo:"publicKeyPem,omitempty"`
	Owner        string `dynamo:"owner,omitempty"`

	// Cursor は cursor パーティションで使う既読位置 (連番)。
	Cursor int `dynamo:"cursor,omitempty"`

	// mylikes / myboosts。ActivityID は Undo の object に詰め直す元の
	// Like/Announce の id。Inbox は配信先 (いいねは対象投稿の著者の inbox)。
	// TargetActor はブーストした投稿の著者 (Undo を直接その inbox にも
	// 届けるために持つ)。TimelineID は自分のブーストをタイムラインに
	// 積んだときの連番で、Undo でそこだけ消すのに使う。Content はいいねした
	// 投稿の本文スナップショット (mylikes のみ)。相手が削除・編集しても
	// /u/:user/favorites に出せるようにいいね時点のものを控える。
	TargetActor string `dynamo:"targetActor,omitempty"`
	TimelineID  int    `dynamo:"timelineID,omitempty"`
	Content     string `dynamo:"content,omitempty"`

	// TTL は Unix 秒。0 なら期限なし。
	TTL int64 `dynamo:"ttl,omitempty"`
}

const (
	FollowStatePending  = "pending"
	FollowStateAccepted = "accepted"
)

const (
	partKey = "id"
	sortKey = "num"

	counterValueKey = "val"

	objectType  = "object--"
	counterType = "counter--"
)

type counterContainer struct {
	Name  string `dynamo:"id"`
	Id    int    `dynamo:"num"`
	Value int    `dynamo:"val"`
}

type objectContainer struct {
	Name string `dynamo:"id"`
	Id   int    `dynamo:"num"`
	Item string `dynamo:"obj"`
}

type client struct {
	table   dynamo.Table
	kvTable dynamo.Table
}

const (
	kvPartKey = "pk"
	kvSortKey = "sk"
)

func (c *client) PutKV(ctx context.Context, item *KVItem) error {
	return c.kvTable.Put(item).Run(ctx)
}

func (c *client) GetKV(ctx context.Context, pk, sk string) (*KVItem, error) {
	item := &KVItem{}
	err := c.kvTable.Get(kvPartKey, pk).Range(kvSortKey, dynamo.Equal, sk).One(ctx, item)
	if err != nil {
		return nil, err
	}
	return item, nil
}

func (c *client) QueryKV(ctx context.Context, pk string) ([]*KVItem, error) {
	items := []*KVItem{}
	if err := c.kvTable.Get(kvPartKey, pk).All(ctx, &items); err != nil {
		return nil, err
	}
	return items, nil
}

func (c *client) DeleteKV(ctx context.Context, pk, sk string) error {
	return c.kvTable.Delete(kvPartKey, pk).Range(kvSortKey, sk).Run(ctx)
}

// CountKV は項目を持ち帰らずに件数だけ数える。フォロワー数の表示に使う。
func (c *client) CountKV(ctx context.Context, pk string) (int, error) {
	n, err := c.kvTable.Get(kvPartKey, pk).Count(ctx)
	return int(n), err
}

func (c *client) Put(ctx context.Context, name string, id int, object interface{}) error {
	b, err := json.Marshal(object)
	if err != nil {
		return err
	}
	container := objectContainer{
		Name: objectType + name,
		Id:   id,
		Item: string(b),
	}
	return c.table.Put(container).Run(ctx)
}

func (c *client) GetObject(ctx context.Context, name string, id int) (*activitystream.Object, error) {
	buf := objectContainer{}
	err := c.table.Get(partKey, objectType+name).Range(sortKey, dynamo.Equal, id).One(ctx, &buf)
	if err != nil {
		return nil, err
	}
	obj := &activitystream.Object{}
	if err := json.Unmarshal([]byte(buf.Item), obj); err != nil {
		return nil, fmt.Errorf("stored object %v/%v is broken: %w", name, id, err)
	}
	return obj, nil
}

// Entry は保存された Object とその連番の対。
type Entry struct {
	ID     int
	Object *activitystream.Object
}

func (c *client) TakeObject(ctx context.Context, name string, base int, cnt int, order Order) ([]*activitystream.Object, error) {
	entries, err := c.TakeEntries(ctx, name, base, cnt, order)
	if err != nil {
		return nil, err
	}
	res := make([]*activitystream.Object, len(entries))
	for i, e := range entries {
		res[i] = e.Object
	}
	return res, nil
}

func (c *client) TakeEntries(ctx context.Context, name string, base int, cnt int, order Order) ([]Entry, error) {
	// base を境界とする範囲指定と、実際に返す並び順の両方を切り替える必要が
	// ある。Order を指定しないと DynamoDB は常に sort key の昇順で返すため、
	// Desc を指定しても古い順に来てしまう。
	rangeOp, ord := dynamo.GreaterOrEqual, dynamo.Ascending
	if order == Desc {
		rangeOp, ord = dynamo.LessOrEqual, dynamo.Descending
	}
	buf := []objectContainer{}
	err := c.table.Get(partKey, objectType+name).
		Range(sortKey, rangeOp, base).
		Order(ord).
		Limit(cnt).
		All(ctx, &buf)
	if err != nil {
		return nil, err
	}
	res := make([]Entry, len(buf))
	for i, v := range buf {
		obj := &activitystream.Object{}
		if err := json.Unmarshal([]byte(v.Item), obj); err != nil {
			return nil, fmt.Errorf("stored object %v/%v is broken: %w", name, v.Id, err)
		}
		res[i] = Entry{ID: v.Id, Object: obj}
	}
	return res, nil
}

func (c *client) DeleteObject(ctx context.Context, name string, id int) error {
	return c.table.Delete(partKey, objectType+name).Range(sortKey, id).Run(ctx)
}

func (c *client) Inc(ctx context.Context, key string) (int, error) {
	buf := counterContainer{}
	// ADD は属性が存在しない場合に 0 からの加算として扱われるので、事前に
	// SetIfNotExists で初期化する必要はなく、1回のアトミックな更新で済む。
	err := c.table.Update(partKey, counterType+key).Range(sortKey, 0).
		Add(counterValueKey, 1).
		Value(ctx, &buf)
	if err != nil {
		return 0, err
	}
	return buf.Value, nil
}

func (c *client) Top(ctx context.Context, key string) (int, error) {
	buf := counterContainer{}
	err := c.table.Get(partKey, counterType+key).Range(sortKey, dynamo.Equal, 0).One(ctx, &buf)
	if err != nil {
		return 0, err
	}
	return buf.Value, nil
}

// NewClient は DynamoDB クライアントを作る。endpoint が空でなければそこを
// 向く (dynamodb-local でのローカル検証用)。
func NewClient(ctx context.Context, region, tableName, kvTableName, endpoint string) (Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	db := dynamo.New(cfg, func(o *dynamodb.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	return &client{
		table:   db.Table(tableName),
		kvTable: db.Table(kvTableName),
	}, nil
}
