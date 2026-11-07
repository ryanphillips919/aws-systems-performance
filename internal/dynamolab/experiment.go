package dynamolab

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

type Measurement struct {
	Method                string  `json:"method"`
	ElapsedMS             float64 `json:"elapsed_ms"`
	Returned              int     `json:"returned"`
	Examined              int64   `json:"examined"`
	Pages                 int     `json:"pages"`
	ReportedCapacityUnits float64 `json:"reported_capacity_units"`
	Items                 []Item  `json:"-"`
}

func (d *DB) Measure(ctx context.Context, method, value string, pageLimit int32) (Measurement, error) {
	m := Measurement{Method: method}
	start := time.Now()
	var next map[string]types.AttributeValue
	expr := map[string]types.AttributeValue{":v": &types.AttributeValueMemberS{Value: value}}
	for {
		var items []map[string]types.AttributeValue
		var n int32
		var capacity *types.ConsumedCapacity
		var err error
		switch method {
		case "user_scan", "code_scan":
			field := "userId"
			if method == "code_scan" {
				field = "shortCode"
			}
			in := &dynamodb.ScanInput{TableName: aws.String(d.Table), FilterExpression: aws.String(field + " = :v"), ExpressionAttributeValues: expr, ExclusiveStartKey: next, ReturnConsumedCapacity: types.ReturnConsumedCapacityTotal}
			if pageLimit > 0 {
				in.Limit = aws.Int32(pageLimit)
			}
			var out *dynamodb.ScanOutput
			out, err = d.Client.Scan(ctx, in)
			if err == nil {
				items = out.Items
				n = out.ScannedCount
				next = out.LastEvaluatedKey
				capacity = out.ConsumedCapacity
			}
		case "user_query":
			in := &dynamodb.QueryInput{TableName: aws.String(d.Table), IndexName: aws.String(Index), KeyConditionExpression: aws.String("userId = :v"), ExpressionAttributeValues: expr, ExclusiveStartKey: next, ScanIndexForward: aws.Bool(false), ReturnConsumedCapacity: types.ReturnConsumedCapacityTotal}
			if pageLimit > 0 {
				in.Limit = aws.Int32(pageLimit)
			}
			var out *dynamodb.QueryOutput
			out, err = d.Client.Query(ctx, in)
			if err == nil {
				items = out.Items
				n = out.ScannedCount
				next = out.LastEvaluatedKey
				capacity = out.ConsumedCapacity
			}
		case "code_get":
			var out *dynamodb.GetItemOutput
			out, err = d.Client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(d.Table), Key: key(value), ReturnConsumedCapacity: types.ReturnConsumedCapacityTotal})
			if err == nil {
				if len(out.Item) > 0 {
					items = []map[string]types.AttributeValue{out.Item}
					n = 1
				}
				capacity = out.ConsumedCapacity
			}
			next = nil
		default:
			return m, fmt.Errorf("unknown method %q", method)
		}
		if err != nil {
			return m, err
		}
		m.Pages++
		m.Examined += int64(n)
		if capacity != nil {
			m.ReportedCapacityUnits += aws.ToFloat64(capacity.CapacityUnits)
		}
		for _, raw := range items {
			var item Item
			if err := attributevalue.UnmarshalMap(raw, &item); err != nil {
				return m, err
			}
			m.Items = append(m.Items, item)
		}
		if len(next) == 0 {
			break
		}
	}
	m.ElapsedMS = float64(time.Since(start).Microseconds()) / 1000
	m.Returned = len(m.Items)
	return m, nil
}
func EqualItems(a, b []Item) bool {
	a = append([]Item(nil), a...)
	b = append([]Item(nil), b...)
	sort.Slice(a, func(i, j int) bool { return a[i].Code < a[j].Code })
	sort.Slice(b, func(i, j int) bool { return b[i].Code < b[j].Code })
	return reflect.DeepEqual(a, b)
}
func (d *DB) Seed(ctx context.Context, count int) error {
	if count < 1 {
		return fmt.Errorf("count must be positive")
	}
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for startIndex := 1; startIndex <= count; startIndex += 25 {
		batch := []types.WriteRequest{}
		for i := startIndex; i < startIndex+25 && i <= count; i++ {
			item := Item{Code: fmt.Sprintf("seed%06d", i), User: fmt.Sprintf("user%03d", i%100), Destination: fmt.Sprintf("https://example.com/%d", i), Created: start.Add(time.Duration(i) * time.Second).Format(TimeFormat)}
			raw, err := attributevalue.MarshalMap(item)
			if err != nil {
				return err
			}
			batch = append(batch, types.WriteRequest{PutRequest: &types.PutRequest{Item: raw}})
		}
		for tries := 0; len(batch) > 0; tries++ {
			if tries >= 10 {
				return fmt.Errorf("unprocessed seed items after retries")
			}
			out, err := d.Client.BatchWriteItem(ctx, &dynamodb.BatchWriteItemInput{RequestItems: map[string][]types.WriteRequest{d.Table: batch}})
			if err != nil {
				return err
			}
			batch = out.UnprocessedItems[d.Table]
			if len(batch) > 0 {
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(time.Duration(1<<tries) * 25 * time.Millisecond):
				}
			}
		}
	}
	return nil
}
func (d *DB) Reset(ctx context.Context) error {
	if d.Table != ExperimentTable {
		return fmt.Errorf("reset allowed only for experiment table")
	}
	_, err := d.Client.DeleteTable(ctx, &dynamodb.DeleteTableInput{TableName: aws.String(d.Table)})
	if err != nil {
		return err
	}
	waiter := dynamodb.NewTableNotExistsWaiter(d.Client)
	if err := waiter.Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(d.Table)}, 60*time.Second, func(o *dynamodb.TableNotExistsWaiterOptions) { o.MinDelay = time.Second; o.MaxDelay = 2 * time.Second }); err != nil {
		return err
	}
	return d.Initialize(ctx)
}
