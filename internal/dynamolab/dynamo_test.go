package dynamolab

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"aws-systems-performance/internal/store"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func TestLocalOnlyEndpoint(t *testing.T) {
	for _, u := range []string{"https://dynamodb.us-east-1.amazonaws.com", "http://example.com:8000", "http://user@localhost:8000", "http://localhost:8000/path"} {
		t.Setenv("DYNAMODB_ENDPOINT", u)
		if _, err := New("x"); err == nil {
			t.Errorf("accepted %q", u)
		}
	}
	t.Setenv("DYNAMODB_ENDPOINT", "http://localhost:8001")
	if _, err := New("x"); err != nil {
		t.Fatal(err)
	}
}
func TestLocalIntegration(t *testing.T) {
	endpoint := os.Getenv("DYNAMODB_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set DYNAMODB_TEST_ENDPOINT to run DynamoDB Local integration tests")
	}
	t.Setenv("DYNAMODB_ENDPOINT", endpoint)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	table := fmt.Sprintf("url-links-test-%d", time.Now().UnixNano())
	db, err := New(table)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, err := db.Client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(table)})
		if err != nil {
			t.Error(err)
		}
	}()
	if err = db.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.Seed(ctx, 250); err != nil {
		t.Fatal(err)
	}
	l, err := db.Get(ctx, "seed000001")
	if err != nil || l.UserID != "user001" {
		t.Fatalf("get %+v %v", l, err)
	}
	if _, err = db.Create(ctx, "seed000001", "x", "https://example.com"); err == nil {
		t.Fatal("duplicate overwrite accepted")
	} else {
		var conditional *types.ConditionalCheckFailedException
		if !errors.As(err, &conditional) {
			t.Fatal(err)
		}
	}
	if _, err = db.Get(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing get: %v", err)
	}
	if _, err = db.Click(ctx, "missing"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing click: %v", err)
	}
	scan, err := db.Measure(ctx, "user_scan", "user001", 7)
	if err != nil {
		t.Fatal(err)
	}
	query, err := db.Measure(ctx, "user_query", "user001", 1)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Pages <= 1 || query.Pages <= 1 || scan.Examined != 250 || query.Returned != 3 || !EqualItems(scan.Items, query.Items) {
		t.Fatalf("pagination or results: scan %+v query %+v", scan, query)
	}
	links, err := db.List(ctx, "user001", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 3 || links[0].Code != "seed000201" || links[1].Code != "seed000101" || links[2].Code != "seed000001" {
		t.Fatalf("order %+v", links)
	}
	codeScan, err := db.Measure(ctx, "code_scan", "seed000001", 7)
	if err != nil {
		t.Fatal(err)
	}
	codeGet, err := db.Measure(ctx, "code_get", "seed000001", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !EqualItems(codeScan.Items, codeGet.Items) {
		t.Fatal("code mismatch")
	}
	const n = 30
	var wg sync.WaitGroup
	ch := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := db.Click(ctx, "seed000001"); ch <- err }()
	}
	wg.Wait()
	close(ch)
	for err := range ch {
		if err != nil {
			t.Fatal(err)
		}
	}
	l, err = db.Get(ctx, "seed000001")
	if err != nil || l.ClickCount != n {
		t.Fatalf("count %+v %v", l, err)
	}
	if _, err = db.Popular(ctx, 10); !errors.Is(err, store.ErrUnsupported) {
		t.Fatalf("popular: %v", err)
	}
}

func TestWaitForActive(t *testing.T) {
	mk := func(table types.TableStatus, index types.IndexStatus) *dynamodb.DescribeTableOutput {
		return &dynamodb.DescribeTableOutput{Table: &types.TableDescription{TableStatus: table, GlobalSecondaryIndexes: []types.GlobalSecondaryIndexDescription{{IndexName: aws.String(Index), IndexStatus: index}}}}
	}
	t.Run("waits for both table and index", func(t *testing.T) {
		states := []*dynamodb.DescribeTableOutput{mk(types.TableStatusCreating, types.IndexStatusCreating), mk(types.TableStatusActive, types.IndexStatusCreating), mk(types.TableStatusActive, types.IndexStatusActive)}
		calls := 0
		err := waitForActive(context.Background(), "test-table", time.Millisecond, func(context.Context) (*dynamodb.DescribeTableOutput, error) {
			state := states[calls]
			calls++
			return state, nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("calls=%d error=%v", calls, err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		err := waitForActive(ctx, "test-table", time.Second, func(context.Context) (*dynamodb.DescribeTableOutput, error) { calls++; return nil, nil })
		if !errors.Is(err, context.Canceled) || calls != 0 {
			t.Fatalf("calls=%d error=%v", calls, err)
		}
	})
	t.Run("timeout with state", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		err := waitForActive(ctx, "test-table", time.Millisecond, func(context.Context) (*dynamodb.DescribeTableOutput, error) {
			return mk(types.TableStatusActive, types.IndexStatusCreating), nil
		})
		if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "GSI=CREATING") {
			t.Fatalf("error=%v", err)
		}
	})
	t.Run("describe error", func(t *testing.T) {
		sentinel := errors.New("describe failure")
		err := waitForActive(context.Background(), "test-table", time.Millisecond, func(context.Context) (*dynamodb.DescribeTableOutput, error) { return nil, sentinel })
		if !errors.Is(err, sentinel) {
			t.Fatalf("error=%v", err)
		}
	})
}
