package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"aws-systems-performance/internal/dynamolab"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
)

func main() {
	mode := flag.String("mode", "compare", "seed, compare, or consistency")
	count := flag.Int("count", 1000, "number of deterministic seed items")
	reset := flag.Bool("reset", false, "delete and recreate the experiment table before seeding")
	user := flag.String("user", "user001", "user to query")
	code := flag.String("code", "seed000001", "code to look up")
	runs := flag.Int("runs", 3, "repetitions")
	page := flag.Int("page-limit", 0, "maximum items evaluated per request (0 uses service default)")
	flag.Parse()
	if *runs < 1 || *page < 0 || *count < 1 {
		log.Fatal("count and runs must be positive; page-limit must be nonnegative")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	db, err := dynamolab.New(dynamolab.ExperimentTable)
	if err != nil {
		log.Fatal(err)
	}
	if err = db.Initialize(ctx); err != nil {
		log.Fatal(err)
	}
	switch *mode {
	case "seed":
		if !*reset {
			log.Fatal("seed requires -reset to produce a known dataset; only the experiment table is deleted")
		}
		if err = db.Reset(ctx); err != nil {
			log.Fatal(err)
		}
		if err = db.Seed(ctx, *count); err != nil {
			log.Fatal(err)
		}
		fmt.Fprintf(os.Stderr, "Seeded %d items into isolated experiment table across 100 users.\n", *count)
	case "consistency":
		unique := fmt.Sprintf("consistency-%d", time.Now().UnixNano())
		if _, err := db.Create(ctx, unique, unique, "https://example.com/consistency"); err != nil {
			log.Fatal(err)
		}
		defer func() {
			_, err := db.Client.DeleteItem(context.Background(), &dynamodb.DeleteItemInput{TableName: aws.String(db.Table), Key: map[string]types.AttributeValue{"shortCode": &types.AttributeValueMemberS{Value: unique}}})
			if err != nil {
				log.Printf("cleanup: %v", err)
			}
		}()
		read := func(strong bool) bool {
			out, err := db.Client.GetItem(ctx, &dynamodb.GetItemInput{TableName: aws.String(db.Table), Key: map[string]types.AttributeValue{"shortCode": &types.AttributeValueMemberS{Value: unique}}, ConsistentRead: aws.Bool(strong)})
			if err != nil {
				log.Fatal(err)
			}
			return len(out.Item) > 0
		}
		eventual, strong := read(false), read(true)
		query, err := db.Measure(ctx, "user_query", unique, 0)
		if err != nil {
			log.Fatal(err)
		}
		result := map[string]any{"base_eventual_found": eventual, "base_strong_found": strong, "gsi_found": query.Returned > 0, "note": "DynamoDB Local does not model real replication lag; seeing the item is not proof of strong GSI consistency."}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			log.Fatal(err)
		}
	case "compare":
		total := 0
		var next map[string]types.AttributeValue
		for {
			out, err := db.Client.Scan(ctx, &dynamodb.ScanInput{TableName: aws.String(db.Table), Select: types.SelectCount, ExclusiveStartKey: next})
			if err != nil {
				log.Fatal(err)
			}
			total += int(out.Count)
			next = out.LastEvaluatedKey
			if len(next) == 0 {
				break
			}
		}
		type run struct {
			Iteration                              int `json:"iteration"`
			UserScan, UserQuery, CodeScan, CodeGet dynamolab.Measurement
		}
		output := struct {
			DatasetItems int    `json:"dataset_items"`
			User         string `json:"user"`
			Code         string `json:"code"`
			PageLimit    int    `json:"page_limit"`
			CapacityNote string `json:"capacity_note"`
			Runs         []run  `json:"runs"`
		}{DatasetItems: total, User: *user, Code: *code, PageLimit: *page, CapacityNote: "DynamoDB Local capacity is simulated and not suitable for AWS cost estimates"}
		for i := 1; i <= *runs; i++ {
			scan, err := db.Measure(ctx, "user_scan", *user, int32(*page))
			if err != nil {
				log.Fatal(err)
			}
			query, err := db.Measure(ctx, "user_query", *user, int32(*page))
			if err != nil {
				log.Fatal(err)
			}
			cs, err := db.Measure(ctx, "code_scan", *code, int32(*page))
			if err != nil {
				log.Fatal(err)
			}
			cg, err := db.Measure(ctx, "code_get", *code, int32(*page))
			if err != nil {
				log.Fatal(err)
			}
			if !dynamolab.EqualItems(scan.Items, query.Items) || !dynamolab.EqualItems(cs.Items, cg.Items) {
				log.Fatal("result mismatch (GSI propagation or concurrent writes may be responsible); retry on a stable dataset")
			}
			if scan.Returned == 0 || cg.Returned != 1 {
				log.Fatal("expected seeded user and code were not found")
			}
			fmt.Fprintf(os.Stderr, "run %d: user scan %d examined / %d returned; query %d / %d; code scan %d / get %d\n", i, scan.Examined, scan.Returned, query.Examined, query.Returned, cs.Examined, cg.Examined)
			output.Runs = append(output.Runs, run{Iteration: i, UserScan: scan, UserQuery: query, CodeScan: cs, CodeGet: cg})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(output); err != nil {
			log.Fatal(err)
		}
	default:
		log.Fatal("mode must be seed, compare, or consistency")
	}
}
