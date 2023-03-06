package datastore

import (
	"encoding/json"
	"math"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/guregu/dynamo"
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
	Put(name string, id int, object interface{}) error
	GetObject(name string, id int) (activitystream.Object, error)
	TakeObject(name string, base int, cnt int, order Order) ([]activitystream.Object, error)

	Inc(key string) (int, error)
	// Top returns count of the key. if key does not exist, return (0, ErrNotFound)
	Top(key string) (int, error)
}

const (
	partKey = "id"
	sortKey = "num"

	counterValueKey = "val"

	objectType  = "object--"
	counterType = "counter--"
)

type base struct {
	Name string `dynamo:"id"`
	Id   int    `dynamo:"num"`
}

type counterContainer struct {
	Base  base
	Value int `dynamo:"val"`
}

type objectContainer struct {
	Base base
	Item string `dynamo:"obj"`
}

type client struct {
	table *dynamo.Table
}

func (c *client) Put(name string, id int, object interface{}) error {
	b, err := json.Marshal(object)
	if err != nil {
		return err
	}
	container := objectContainer{
		Base: base{
			Name: objectType + name,
			Id:   id,
		},
		Item: string(b),
	}
	return c.table.Put(container).Run()
}

func (c *client) GetObject(name string, id int) (activitystream.Object, error) {
	buf := objectContainer{}
	err := c.table.Get(partKey, objectType+name).Range(sortKey, dynamo.Equal, id).One(&buf)
	obj := activitystream.Object{}
	json.Unmarshal([]byte(buf.Item), &obj)
	return obj, err
}

func (c *client) TakeObject(name string, base int, cnt int, order Order) ([]activitystream.Object, error) {
	ord := dynamo.GreaterOrEqual
	if order == Desc {
		ord = dynamo.LessOrEqual
	}
	buf := []objectContainer{}
	err := c.table.Get(partKey, objectType+name).Range(sortKey, ord, base).Limit(int64(cnt)).All(&buf)
	res := make([]activitystream.Object, len(buf))
	for i, v := range buf {
		obj := activitystream.Object{}
		json.Unmarshal([]byte(v.Item), &obj)
		res[i] = obj
	}
	return res, err
}

func (c *client) Inc(key string) (int, error) {
	// ensure exists
	buf := counterContainer{}
	dynamoKey := counterType + key
	err := c.table.Update(partKey, dynamoKey).Range(sortKey, 0).SetIfNotExists(counterValueKey, 0).Value(&buf)
	if err != nil {
		return -1, err
	}
	err = c.table.Update(partKey, dynamoKey).Range(sortKey, 0).SetExpr("'"+counterValueKey+"' = '"+counterValueKey+"' + ?", 1).Value(&buf)
	return buf.Value, err
}

func (c *client) Top(key string) (int, error) {
	buf := counterContainer{}
	dynamoKey := counterType + key
	err := c.table.Get(partKey, dynamoKey).Range(sortKey, dynamo.Equal, 0).One(&buf)
	return buf.Value, err
}

func NewClient(region, tableName string) (Client, error) {
	t, err := table(region, tableName)
	if err != nil {
		return nil, err
	}
	return &client{table: t}, nil
}

func table(region, tableName string) (*dynamo.Table, error) {
	cfg := aws.NewConfig().WithRegion(region)
	s, err := session.NewSession()
	if err != nil {
		return nil, err
	}
	db := dynamo.New(s, cfg)
	t := db.Table(tableName)
	return &t, nil
}
