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

	Inc(ctx context.Context, key string) (int, error)
	// Top returns count of the key. if key does not exist, return (0, ErrNotFound)
	Top(ctx context.Context, key string) (int, error)
}

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
	table dynamo.Table
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

func (c *client) TakeObject(ctx context.Context, name string, base int, cnt int, order Order) ([]*activitystream.Object, error) {
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
	res := make([]*activitystream.Object, len(buf))
	for i, v := range buf {
		res[i] = &activitystream.Object{}
		if err := json.Unmarshal([]byte(v.Item), res[i]); err != nil {
			return nil, fmt.Errorf("stored object %v/%v is broken: %w", name, v.Id, err)
		}
	}
	return res, nil
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
func NewClient(ctx context.Context, region, tableName, endpoint string) (Client, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		return nil, err
	}
	db := dynamo.New(cfg, func(o *dynamodb.Options) {
		if endpoint != "" {
			o.BaseEndpoint = aws.String(endpoint)
		}
	})
	return &client{table: db.Table(tableName)}, nil
}
