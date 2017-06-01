// Licensed to Pulumi Corporation ("Pulumi") under one or more
// contributor license agreements.  See the NOTICE file distributed with
// this work for additional information regarding copyright ownership.
// Pulumi licenses this file to You under the Apache License, Version 2.0
// (the "License"); you may not use this file except in compliance with
// the License.  You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package dynamodb

import (
	"crypto/sha1"
	"fmt"
	"reflect"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/awserr"
	awsdynamodb "github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/pulumi/lumi/pkg/resource"
	"github.com/pulumi/lumi/pkg/util/contract"
	"github.com/pulumi/lumi/pkg/util/mapper"
	"github.com/pulumi/lumi/sdk/go/pkg/lumirpc"
	"golang.org/x/net/context"

	"github.com/pulumi/lumi/lib/aws/provider/arn"
	"github.com/pulumi/lumi/lib/aws/provider/awsctx"
	"github.com/pulumi/lumi/lib/aws/rpc/dynamodb"
)

const TableToken = dynamodb.TableToken

// constants for the various table limits.
const (
	minTableName              = 3
	maxTableName              = 255
	minTableAttributeName     = 1
	maxTableAttributeName     = 255
	minReadCapacity           = 1
	minWriteCapacity          = 1
	maxGlobalSecondaryIndexes = 5
)

const (
	// hashKeyAttribute is a partition key, also known as its hash attribute.  The term "hash attribute" derives from
	// DynamoDB's usage of an internal hash function to evenly distribute data items across partitions based on their
	// partition key values.
	hashKeyAttribute = "HASH"
	// rangeKeyType is a sort key, also known as its range attribute.  The term "range attribute" derives from the way
	// DynamoDB stores items with the same partition key physically close together, sorted by the sort key value.
	rangeKeyAttribute = "RANGE"
)

// NewTableProvider creates a provider that handles DynamoDB Table operations.
func NewTableProvider(ctx *awsctx.Context) lumirpc.ResourceProviderServer {
	ops := &tableProvider{ctx}
	return dynamodb.NewTableProvider(ops)
}

type tableProvider struct {
	ctx *awsctx.Context
}

// Check validates that the given property bag is valid for a resource of the given type.
func (p *tableProvider) Check(ctx context.Context, obj *dynamodb.Table) ([]mapper.FieldError, error) {
	var failures []mapper.FieldError

	if name := obj.TableName; name != nil {
		if len(*name) < minTableName {
			failures = append(failures,
				mapper.NewFieldErr(reflect.TypeOf(obj), dynamodb.Table_Name,
					fmt.Errorf("less than minimum length of %v", minTableName)))
		}
		if len(*name) > maxTableName {
			failures = append(failures,
				mapper.NewFieldErr(reflect.TypeOf(obj), dynamodb.Table_Name,
					fmt.Errorf("exceeded maximum length of %v", maxTableName)))
		}
		// TODO: check the vailidity of names ([a-zA-Z0-9_.-]+).
	}

	if obj.ReadCapacity < minReadCapacity {
		failures = append(failures,
			mapper.NewFieldErr(reflect.TypeOf(obj), dynamodb.Table_ReadCapacity,
				fmt.Errorf("less than minimum of %v", minReadCapacity)))
	}
	if obj.WriteCapacity < minWriteCapacity {
		failures = append(failures,
			mapper.NewFieldErr(reflect.TypeOf(obj), dynamodb.Table_WriteCapacity,
				fmt.Errorf("less than minimum of %v", minWriteCapacity)))
	}

	for _, attribute := range obj.Attributes {
		if len(attribute.Name) < minTableAttributeName {
			failures = append(failures,
				mapper.NewFieldErr(reflect.TypeOf(attribute), dynamodb.Attribute_Name,
					fmt.Errorf("less than minimum length of %v", minTableAttributeName)))
		}
		if len(attribute.Name) > maxTableAttributeName {
			failures = append(failures,
				mapper.NewFieldErr(reflect.TypeOf(attribute), dynamodb.Attribute_Name,
					fmt.Errorf("exceeded maximum length of %v", maxTableAttributeName)))
		}
		switch attribute.Type {
		case "S", "N", "B":
			break
		default:
			failures = append(failures,
				mapper.NewFieldErr(reflect.TypeOf(attribute), dynamodb.Attribute_Type,
					fmt.Errorf("not one of valid values S (string), N (number) or B (binary)")))
		}
	}

	if obj.GlobalSecondaryIndexes != nil {
		gsis := *obj.GlobalSecondaryIndexes
		if len(gsis) > maxGlobalSecondaryIndexes {
			failures = append(failures,
				mapper.NewFieldErr(reflect.TypeOf(obj), dynamodb.Table_GlobalSecondaryIndexes,
					fmt.Errorf("more than %v global secondary indexes requested", maxGlobalSecondaryIndexes)))
		}
		for _, gsi := range gsis {
			name := gsi.IndexName
			if len(name) < minTableName {
				failures = append(failures,
					mapper.NewFieldErr(reflect.TypeOf(gsi), dynamodb.GlobalSecondaryIndex_IndexName,
						fmt.Errorf("less than minimum length of %v", minTableName)))
			}
			if len(name) > maxTableName {
				failures = append(failures,
					mapper.NewFieldErr(reflect.TypeOf(gsi), dynamodb.GlobalSecondaryIndex_IndexName,
						fmt.Errorf("exceeded maximum length of %v", maxTableName)))
			}
			if gsi.ReadCapacity < minReadCapacity {
				failures = append(failures,
					mapper.NewFieldErr(reflect.TypeOf(gsi), dynamodb.GlobalSecondaryIndex_ReadCapacity,
						fmt.Errorf("less than minimum of %v", minReadCapacity)))
			}
			if gsi.WriteCapacity < minWriteCapacity {
				failures = append(failures,
					mapper.NewFieldErr(reflect.TypeOf(gsi), dynamodb.GlobalSecondaryIndex_WriteCapacity,
						fmt.Errorf("less than minimum of %v", minWriteCapacity)))
			}
		}
	}

	return failures, nil
}

// Create allocates a new instance of the provided resource and returns its unique ID afterwards.  (The input ID
// must be blank.)  If this call fails, the resource must not have been created (i.e., it is "transacational").
func (p *tableProvider) Create(ctx context.Context, obj *dynamodb.Table) (resource.ID, error) {
	// If an explicit name is given, use it.  Otherwise, auto-generate a name in part based on the resource name.
	var name string
	if obj.TableName != nil {
		name = *obj.TableName
	} else {
		name = resource.NewUniqueHex(*obj.Name+"-", maxTableName, sha1.Size)
	}

	var attributeDefinitions []*awsdynamodb.AttributeDefinition
	for _, attr := range obj.Attributes {
		attributeDefinitions = append(attributeDefinitions, &awsdynamodb.AttributeDefinition{
			AttributeName: aws.String(attr.Name),
			AttributeType: aws.String(string(attr.Type)),
		})
	}

	fmt.Printf("Creating DynamoDB Table '%v' with name '%v'\n", obj.Name, name)
	keySchema := []*awsdynamodb.KeySchemaElement{
		{
			AttributeName: aws.String(obj.HashKey),
			KeyType:       aws.String(hashKeyAttribute),
		},
	}
	if obj.RangeKey != nil {
		keySchema = append(keySchema, &awsdynamodb.KeySchemaElement{
			AttributeName: obj.RangeKey,
			KeyType:       aws.String(rangeKeyAttribute),
		})
	}
	create := &awsdynamodb.CreateTableInput{
		TableName:            aws.String(name),
		AttributeDefinitions: attributeDefinitions,
		KeySchema:            keySchema,
		ProvisionedThroughput: &awsdynamodb.ProvisionedThroughput{
			ReadCapacityUnits:  aws.Int64(int64(obj.ReadCapacity)),
			WriteCapacityUnits: aws.Int64(int64(obj.WriteCapacity)),
		},
	}
	if obj.GlobalSecondaryIndexes != nil {
		var gsis []*awsdynamodb.GlobalSecondaryIndex
		for _, gsi := range *obj.GlobalSecondaryIndexes {
			keySchema := []*awsdynamodb.KeySchemaElement{
				{
					AttributeName: aws.String(gsi.HashKey),
					KeyType:       aws.String(hashKeyAttribute),
				},
			}
			if gsi.RangeKey != nil {
				keySchema = append(keySchema, &awsdynamodb.KeySchemaElement{
					AttributeName: gsi.RangeKey,
					KeyType:       aws.String(rangeKeyAttribute),
				})
			}
			gsis = append(gsis, &awsdynamodb.GlobalSecondaryIndex{
				IndexName: aws.String(gsi.IndexName),
				KeySchema: keySchema,
				ProvisionedThroughput: &awsdynamodb.ProvisionedThroughput{
					ReadCapacityUnits:  aws.Int64(int64(gsi.ReadCapacity)),
					WriteCapacityUnits: aws.Int64(int64(gsi.WriteCapacity)),
				},
				Projection: &awsdynamodb.Projection{
					NonKeyAttributes: aws.StringSlice(gsi.NonKeyAttributes),
					ProjectionType:   aws.String(string(gsi.ProjectionType)),
				},
			})
		}
		create.GlobalSecondaryIndexes = gsis
	}

	// Now go ahead and perform the action.
	var arn string
	if resp, err := p.ctx.DynamoDB().CreateTable(create); err != nil {
		return "", err
	} else {
		contract.Assert(resp != nil)
		contract.Assert(resp.TableDescription != nil)
		contract.Assert(resp.TableDescription.TableArn != nil)
		arn = *resp.TableDescription.TableArn
	}

	// Wait for the table to be ready and then return the ID (just its name).
	fmt.Printf("DynamoDB Table created: %v; waiting for it to become active\n", name)
	if err := p.waitForTableState(name, true); err != nil {
		return "", err
	}
	return resource.ID(arn), nil
}

// Get reads the instance state identified by ID, returning a populated resource object, or an error if not found.
func (p *tableProvider) Get(ctx context.Context, id resource.ID) (*dynamodb.Table, error) {
	name, err := arn.ParseResourceName(id)
	if err != nil {
		if awsctx.IsAWSError(err, "ResourceNotFoundException") {
			return nil, nil
		}
		return nil, err
	}
	resp, err := p.ctx.DynamoDB().DescribeTable(&awsdynamodb.DescribeTableInput{TableName: aws.String(name)})
	if err != nil {
		return nil, err
	}

	// The object was found, we need to reverse map a bunch of properties into the structure form.
	contract.Assert(resp != nil)
	contract.Assert(resp.Table != nil)
	tab := resp.Table

	var attributes []dynamodb.Attribute
	for _, attr := range tab.AttributeDefinitions {
		attributes = append(attributes, dynamodb.Attribute{
			Name: *attr.AttributeName,
			Type: dynamodb.AttributeType(*attr.AttributeType),
		})
	}

	hashKey, rangeKey := getHashRangeKeys(tab.KeySchema)

	var gsis *[]dynamodb.GlobalSecondaryIndex
	if len(tab.GlobalSecondaryIndexes) > 0 {
		var gis []dynamodb.GlobalSecondaryIndex
		for _, gsid := range tab.GlobalSecondaryIndexes {
			hk, rk := getHashRangeKeys(gsid.KeySchema)
			gis = append(gis, dynamodb.GlobalSecondaryIndex{
				IndexName:        *gsid.IndexName,
				HashKey:          hk,
				ReadCapacity:     float64(*gsid.ProvisionedThroughput.ReadCapacityUnits),
				WriteCapacity:    float64(*gsid.ProvisionedThroughput.WriteCapacityUnits),
				RangeKey:         rk,
				NonKeyAttributes: aws.StringValueSlice(gsid.Projection.NonKeyAttributes),
				ProjectionType:   dynamodb.ProjectionType(*gsid.Projection.ProjectionType),
			})
		}
		gsis = &gis
	}

	return &dynamodb.Table{
		HashKey:                hashKey,
		Attributes:             attributes,
		ReadCapacity:           float64(*tab.ProvisionedThroughput.ReadCapacityUnits),
		WriteCapacity:          float64(*tab.ProvisionedThroughput.WriteCapacityUnits),
		RangeKey:               rangeKey,
		TableName:              tab.TableName,
		GlobalSecondaryIndexes: gsis,
	}, nil
}

func getHashRangeKeys(schema []*awsdynamodb.KeySchemaElement) (string, *string) {
	var hashKey *string
	var rangeKey *string
	for _, elem := range schema {
		switch *elem.KeyType {
		case hashKeyAttribute:
			hashKey = elem.AttributeName
		case rangeKeyAttribute:
			rangeKey = elem.AttributeName
		default:
			contract.Failf("Unexpected key schema attribute type: %v", *elem.KeyType)
		}
	}
	contract.Assertf(hashKey != nil, "Expected to discover a hash partition key")
	return *hashKey, rangeKey
}

// InspectChange checks what impacts a hypothetical update will have on the resource's properties.
func (p *tableProvider) InspectChange(ctx context.Context, id resource.ID,
	old *dynamodb.Table, new *dynamodb.Table, diff *resource.ObjectDiff) ([]string, error) {
	return nil, nil
}

// Update updates an existing resource with new values.  Only those values in the provided property bag are updated
// to new values.  The resource ID is returned and may be different if the resource had to be recreated.
func (p *tableProvider) Update(ctx context.Context, id resource.ID,
	old *dynamodb.Table, new *dynamodb.Table, diff *resource.ObjectDiff) error {
	name, err := arn.ParseResourceName(id)
	if err != nil {
		return err
	}

	// Note: Changing dynamodb.Table_Attributes alone does not trigger an update on the resource, it must be changed
	// along with using the new attributes in an index.  The latter will process the update.

	// Per DynamoDB documention at http://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_UpdateTable.html:

	// You can only perform one of the following operations at once:
	// * Modify the provisioned throughput settings of the table.
	// * Enable or disable Streams on the table.
	// * Remove a global secondary index from the table.
	// * Create a new global secondary index on the table. Once the index begins backfilling, you can use
	//   UpdateTable to perform other operations.

	// So we have to serialize each of the requested updates and potentially make multiple calls to UpdateTable, waiting
	// for the Table to reach the ready state between calls.

	// First modify provisioned throughput if needed.
	if diff.Changed(dynamodb.Table_ReadCapacity) || diff.Changed(dynamodb.Table_WriteCapacity) {
		fmt.Printf("Updating provisioned capacity for DynamoDB Table %v\n", aws.String(name))
		update := &awsdynamodb.UpdateTableInput{
			TableName: aws.String(name),
			ProvisionedThroughput: &awsdynamodb.ProvisionedThroughput{
				ReadCapacityUnits:  aws.Int64(int64(new.ReadCapacity)),
				WriteCapacityUnits: aws.Int64(int64(new.WriteCapacity)),
			},
		}
		if err := p.updateTable(name, update); err != nil {
			return err
		}
	}

	// Next, delete and create global secondary indexes.
	if diff.Changed(dynamodb.Table_GlobalSecondaryIndexes) {
		newGlobalSecondaryIndexes := newGlobalSecondaryIndexHashSet(new.GlobalSecondaryIndexes)
		oldGlobalSecondaryIndexes := newGlobalSecondaryIndexHashSet(old.GlobalSecondaryIndexes)
		d := oldGlobalSecondaryIndexes.Diff(newGlobalSecondaryIndexes)
		// First, add any new indexes
		for _, o := range d.Adds() {
			gsi := o.(globalSecondaryIndexHash).item
			fmt.Printf("Adding new global secondary index %v for DynamoDB Table %v\n", gsi.IndexName, name)
			keySchema := []*awsdynamodb.KeySchemaElement{
				{
					AttributeName: aws.String(gsi.HashKey),
					KeyType:       aws.String(hashKeyAttribute),
				},
			}
			if gsi.RangeKey != nil {
				keySchema = append(keySchema, &awsdynamodb.KeySchemaElement{
					AttributeName: gsi.RangeKey,
					KeyType:       aws.String(rangeKeyAttribute),
				})
			}
			var attributeDefinitions []*awsdynamodb.AttributeDefinition
			for _, attr := range new.Attributes {
				attributeDefinitions = append(attributeDefinitions, &awsdynamodb.AttributeDefinition{
					AttributeName: aws.String(attr.Name),
					AttributeType: aws.String(string(attr.Type)),
				})
			}
			update := &awsdynamodb.UpdateTableInput{
				TableName:            aws.String(name),
				AttributeDefinitions: attributeDefinitions,
				GlobalSecondaryIndexUpdates: []*awsdynamodb.GlobalSecondaryIndexUpdate{
					{
						Create: &awsdynamodb.CreateGlobalSecondaryIndexAction{
							IndexName: aws.String(gsi.IndexName),
							KeySchema: keySchema,
							ProvisionedThroughput: &awsdynamodb.ProvisionedThroughput{
								ReadCapacityUnits:  aws.Int64(int64(gsi.ReadCapacity)),
								WriteCapacityUnits: aws.Int64(int64(gsi.WriteCapacity)),
							},
							Projection: &awsdynamodb.Projection{
								NonKeyAttributes: aws.StringSlice(gsi.NonKeyAttributes),
								ProjectionType:   aws.String(string(gsi.ProjectionType)),
							},
						},
					},
				},
			}
			if err := p.updateTable(name, update); err != nil {
				return err
			}
		}
		// Next, modify provisioned throughput on any updated indexes
		for _, o := range d.Updates() {
			gsi := o.(globalSecondaryIndexHash).item
			fmt.Printf("Updating capacity for global secondary index %v for DynamoDB Table %v\n", gsi.IndexName, name)
			update := &awsdynamodb.UpdateTableInput{
				TableName: aws.String(name),
				GlobalSecondaryIndexUpdates: []*awsdynamodb.GlobalSecondaryIndexUpdate{
					{
						Update: &awsdynamodb.UpdateGlobalSecondaryIndexAction{
							IndexName: aws.String(gsi.IndexName),
							ProvisionedThroughput: &awsdynamodb.ProvisionedThroughput{
								ReadCapacityUnits:  aws.Int64(int64(gsi.ReadCapacity)),
								WriteCapacityUnits: aws.Int64(int64(gsi.WriteCapacity)),
							},
						},
					},
				},
			}
			if err := p.updateTable(name, update); err != nil {
				return err
			}
		}
		// Finally, delete and removed indexes
		for _, o := range d.Deletes() {
			gsi := o.(globalSecondaryIndexHash).item
			fmt.Printf("Deleting global secondary index %v for DynamoDB Table %v\n", gsi.IndexName, name)
			update := &awsdynamodb.UpdateTableInput{
				TableName: aws.String(name),
				GlobalSecondaryIndexUpdates: []*awsdynamodb.GlobalSecondaryIndexUpdate{
					{
						Delete: &awsdynamodb.DeleteGlobalSecondaryIndexAction{
							IndexName: aws.String(gsi.IndexName),
						},
					},
				},
			}
			if err := p.updateTable(name, update); err != nil {
				return err
			}
		}

		if err := p.waitForTableState(name, true); err != nil {
			return err
		}
	}
	return nil
}

// Delete tears down an existing resource with the given ID.  If it fails, the resource is assumed to still exist.
func (p *tableProvider) Delete(ctx context.Context, id resource.ID) error {
	name, err := arn.ParseResourceName(id)
	if err != nil {
		return err
	}

	// First, perform the deletion.
	fmt.Printf("Deleting DynamoDB Table '%v'\n", name)
	succ, err := awsctx.RetryUntilLong(
		p.ctx,
		func() (bool, error) {
			_, err := p.ctx.DynamoDB().DeleteTable(&awsdynamodb.DeleteTableInput{
				TableName: aws.String(name),
			})
			if err != nil {
				if awsctx.IsAWSError(err, awsdynamodb.ErrCodeResourceNotFoundException) {
					return true, nil
				} else if awsctx.IsAWSError(err, awsdynamodb.ErrCodeResourceInUseException) {
					return false, nil
				}
				return false, err // anything else is a real error; propagate it.
			}
			return true, nil
		},
	)
	if err != nil {
		return err
	}
	if !succ {
		return fmt.Errorf("DynamoDB table '%v' could not be deleted", name)
	}

	// Wait for the table to actually become deleted before returning.
	fmt.Printf("DynamoDB Table delete request submitted; waiting for it to delete\n")
	return p.waitForTableState(name, false)
}

func (p *tableProvider) updateTable(name string, update *awsdynamodb.UpdateTableInput) error {
	succ, err := awsctx.RetryUntil(
		p.ctx,
		func() (bool, error) {
			_, err := p.ctx.DynamoDB().UpdateTable(update)
			if err != nil {
				if awsctx.IsAWSError(err, "ResourceNotFoundException", "ResourceInUseException") {
					fmt.Printf("Waiting to update resource '%v': %v", name, err.(awserr.Error).Message())
					return false, nil
				}
				return false, err // anything else is a real error; propagate it.
			}
			return true, nil
		},
	)
	if err != nil {
		return err
	}
	if !succ {
		return fmt.Errorf("DynamoDB table '%v' could not be updated", name)
	}
	if err := p.waitForTableState(name, true); err != nil {
		return err
	}
	return nil
}

func (p *tableProvider) waitForTableState(name string, exist bool) error {
	succ, err := awsctx.RetryUntilLong(
		p.ctx,
		func() (bool, error) {
			description, err := p.ctx.DynamoDB().DescribeTable(&awsdynamodb.DescribeTableInput{
				TableName: aws.String(name),
			})

			if err != nil {
				if awsctx.IsAWSError(err, "ResourceNotFoundException") {
					// The table is missing; if exist==false, we're good, otherwise keep retrying.
					return !exist, nil
				}
				return false, err // anything other than "ResourceNotFoundException" is a real error; propagate it.
			}

			if exist && aws.StringValue(description.Table.TableStatus) != "ACTIVE" {
				return false, nil
			}

			// If we got here, the table was found and was ACTIVE if exist is true; if exist==true, we're good; else, keep retrying.
			return exist, nil
		},
	)
	if err != nil {
		return err
	}
	if !succ {
		var reason string
		if exist {
			reason = "active"
		} else {
			reason = "deleted"
		}
		return fmt.Errorf("DynamoDB table '%v' did not become %v", name, reason)
	}
	return nil
}

type globalSecondaryIndexHash struct {
	item dynamodb.GlobalSecondaryIndex
}

var _ awsctx.Hashable = globalSecondaryIndexHash{}

func (option globalSecondaryIndexHash) HashKey() awsctx.Hash {
	return awsctx.Hash(option.item.IndexName)
}
func (option globalSecondaryIndexHash) HashValue() awsctx.Hash {
	return awsctx.Hash(string(int(option.item.ReadCapacity)) + ":" + string(int(option.item.WriteCapacity)))
}
func newGlobalSecondaryIndexHashSet(options *[]dynamodb.GlobalSecondaryIndex) *awsctx.HashSet {
	set := awsctx.NewHashSet()
	if options == nil {
		return set
	}
	for _, option := range *options {
		set.Add(globalSecondaryIndexHash{option})
	}
	return set
}