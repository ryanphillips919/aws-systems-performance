// Package dynamolab implements the deliberately local-only DynamoDB experiment.
package dynamolab

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"aws-systems-performance/internal/store"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

const Index = "UserLinksIndex"
const TimeFormat = "2006-01-02T15:04:05.000000000Z"
const DefaultTable = "url-links"
const ExperimentTable = "url-links-experiment"

type DB struct {
	Client *dynamodb.Client
	Table  string
}
type Item struct {
	Code        string `dynamodbav:"shortCode"`
	User        string `dynamodbav:"userId"`
	Destination string `dynamodbav:"destinationUrl"`
	Created     string `dynamodbav:"createdAt"`
	Clicks      int64  `dynamodbav:"clickCount"`
}

func (i Item) Link() (store.Link, error) {
	t, err := time.Parse(TimeFormat, i.Created)
	return store.Link{Code: i.Code, UserID: i.User, DestinationURL: i.Destination, CreatedAt: t, ClickCount: i.Clicks}, err
}
func New(table string) (*DB, error) {
	endpoint := os.Getenv("DYNAMODB_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://localhost:8001"
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, fmt.Errorf("DYNAMODB_ENDPOINT must be a local HTTP endpoint")
	}
	switch strings.ToLower(u.Hostname()) {
	case "localhost", "127.0.0.1", "::1", "dynamodb":
	default:
		return nil, fmt.Errorf("DYNAMODB_ENDPOINT host must be localhost, 127.0.0.1, ::1, or dynamodb")
	}
	if table == "" {
		table = DefaultTable
	}
	cfg := aws.Config{Region: "us-west-2", Credentials: credentials.NewStaticCredentialsProvider("local", "local", "")}
	return &DB{Client: dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) { o.BaseEndpoint = aws.String(endpoint) }), Table: table}, nil
}
func (d *DB) Initialize(ctx context.Context) error {
	var err error
	for attempt := 0; attempt < 30; attempt++ {
		_, err = d.Client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(d.Table)})
		if err == nil {
			break
		}
		var missing *types.ResourceNotFoundException
		if errors.As(err, &missing) {
			_, err = d.Client.CreateTable(ctx, &dynamodb.CreateTableInput{TableName: aws.String(d.Table), BillingMode: types.BillingModePayPerRequest,
				AttributeDefinitions:   []types.AttributeDefinition{{AttributeName: aws.String("shortCode"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("userId"), AttributeType: types.ScalarAttributeTypeS}, {AttributeName: aws.String("createdAt"), AttributeType: types.ScalarAttributeTypeS}},
				KeySchema:              []types.KeySchemaElement{{AttributeName: aws.String("shortCode"), KeyType: types.KeyTypeHash}},
				GlobalSecondaryIndexes: []types.GlobalSecondaryIndex{{IndexName: aws.String(Index), KeySchema: []types.KeySchemaElement{{AttributeName: aws.String("userId"), KeyType: types.KeyTypeHash}, {AttributeName: aws.String("createdAt"), KeyType: types.KeyTypeRange}}, Projection: &types.Projection{ProjectionType: types.ProjectionTypeAll}}},
			})
			var exists *types.ResourceInUseException
			if err != nil && !errors.As(err, &exists) {
				return fmt.Errorf("create table: %w", err)
			}
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	if err != nil {
		var exists *types.ResourceInUseException
		if !errors.As(err, &exists) {
			return fmt.Errorf("connect to DynamoDB Local: %w", err)
		}
	}
	// Bound readiness separately while preserving any earlier caller deadline.
	readyCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	return waitForActive(readyCtx, d.Table, time.Second, func(ctx context.Context) (*dynamodb.DescribeTableOutput, error) {
		return d.Client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(d.Table)})
	})
}

// waitForActive waits for both the table and its GSI; table readiness alone
// does not guarantee that the index can serve queries.
func waitForActive(ctx context.Context, table string, interval time.Duration, describe func(context.Context) (*dynamodb.DescribeTableOutput, error)) error {
	last := "not yet described"
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("waiting for table %s and GSI %s to become ACTIVE (last state: %s): %w", table, Index, last, err)
		}
		out, err := describe(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return fmt.Errorf("waiting for table %s and GSI %s to become ACTIVE (last state: %s): %w", table, Index, last, ctx.Err())
			}
			var missing *types.ResourceNotFoundException
			if !errors.As(err, &missing) {
				return fmt.Errorf("describe table %s while waiting for GSI %s: %w", table, Index, err)
			}
			last = "table not yet visible"
		} else if out == nil || out.Table == nil {
			return fmt.Errorf("describe table %s: missing table description", table)
		} else {
			last = fmt.Sprintf("table=%s, GSI=not present", out.Table.TableStatus)
			for _, index := range out.Table.GlobalSecondaryIndexes {
				if aws.ToString(index.IndexName) != Index {
					continue
				}
				last = fmt.Sprintf("table=%s, GSI=%s", out.Table.TableStatus, index.IndexStatus)
				if out.Table.TableStatus == types.TableStatusActive && index.IndexStatus == types.IndexStatusActive {
					return nil
				}
				if index.IndexStatus == types.IndexStatusDeleting {
					return fmt.Errorf("waiting for table %s: GSI %s is deleting", table, Index)
				}
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return fmt.Errorf("waiting for table %s and GSI %s to become ACTIVE (last state: %s): %w", table, Index, last, ctx.Err())
		case <-timer.C:
		}
	}
}

func dependencyError(err error) error {
	var network net.Error
	if err != nil && (errors.As(err, &network) || errors.Is(err, context.DeadlineExceeded)) {
		return fmt.Errorf("%w: %w", store.ErrUnavailable, err)
	}
	return err
}

func (d *DB) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := d.Client.DescribeTable(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(d.Table)})
	if err != nil {
		return dependencyError(err)
	}
	if out.Table == nil || out.Table.TableStatus != types.TableStatusActive {
		return fmt.Errorf("%w: table not active", store.ErrUnavailable)
	}
	return nil
}
func key(code string) map[string]types.AttributeValue {
	return map[string]types.AttributeValue{"shortCode": &types.AttributeValueMemberS{Value: code}}
}
func decode(m map[string]types.AttributeValue) (store.Link, error) {
	var i Item
	if err := attributevalue.UnmarshalMap(m, &i); err != nil {
		return store.Link{}, err
	}
	return i.Link()
}
func (d *DB) Create(ctx context.Context, code, user, destination string) (store.Link, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	item := Item{Code: code, User: user, Destination: destination, Created: time.Now().UTC().Format(TimeFormat)}
	m, err := attributevalue.MarshalMap(item)
	if err != nil {
		return store.Link{}, err
	}
	_, err = d.Client.PutItem(ctx, &dynamodb.PutItemInput{TableName: aws.String(d.Table), Item: m, ConditionExpression: aws.String("attribute_not_exists(shortCode)")})
	if err != nil {
		return store.Link{}, dependencyError(err)
	}
	return item.Link()
}
func (d *DB) Get(ctx context.Context, code string) (store.Link, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := d.Client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(d.Table), Key: key(code), ConsistentRead: aws.Bool(true)})
	if err != nil {
		return store.Link{}, dependencyError(err)
	}
	if len(out.Item) == 0 {
		return store.Link{}, store.ErrNotFound
	}
	return decode(out.Item)
}
func (d *DB) Click(ctx context.Context, code string) (store.Link, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := d.Client.UpdateItem(ctx, &dynamodb.UpdateItemInput{TableName: aws.String(d.Table), Key: key(code), UpdateExpression: aws.String("ADD clickCount :one"), ConditionExpression: aws.String("attribute_exists(shortCode)"), ExpressionAttributeValues: map[string]types.AttributeValue{":one": &types.AttributeValueMemberN{Value: "1"}}, ReturnValues: types.ReturnValueAllNew})
	var missing *types.ConditionalCheckFailedException
	if errors.As(err, &missing) {
		return store.Link{}, store.ErrNotFound
	}
	if err != nil {
		return store.Link{}, dependencyError(err)
	}
	return decode(out.Attributes)
}
func (d *DB) List(ctx context.Context, user string, limit int) ([]store.Link, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	links := make([]store.Link, 0)
	var next map[string]types.AttributeValue
	for len(links) < limit {
		out, err := d.Client.Query(ctx, &dynamodb.QueryInput{TableName: aws.String(d.Table), IndexName: aws.String(Index), KeyConditionExpression: aws.String("userId = :u"), ExpressionAttributeValues: map[string]types.AttributeValue{":u": &types.AttributeValueMemberS{Value: user}}, ScanIndexForward: aws.Bool(false), Limit: aws.Int32(int32(limit - len(links))), ExclusiveStartKey: next})
		if err != nil {
			return nil, dependencyError(err)
		}
		for _, item := range out.Items {
			l, err := decode(item)
			if err != nil {
				return nil, err
			}
			links = append(links, l)
		}
		next = out.LastEvaluatedKey
		if len(next) == 0 {
			break
		}
	}
	return links, nil
}
func (d *DB) Popular(context.Context, int) ([]store.Link, error) { return nil, store.ErrUnsupported }
