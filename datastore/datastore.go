package datastore

import (
	"encoding/json"
	"math"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/guregu/dynamo"
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
	Get(name string, id int) (string, error)
	Take(name string, base int, cnt int, order Order) ([]string, error)

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

func (c *client) Get(name string, id int) (string, error) {
	buf := objectContainer{}
	err := c.table.Get(partKey, objectType+name).Range(sortKey, dynamo.Equal, id).One(&buf)
	return buf.Item, err
}

func (c *client) Take(name string, base int, cnt int, order Order) ([]string, error) {
	return []string{}, nil
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
