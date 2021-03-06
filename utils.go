package dynami

import (
	"fmt"
	"math/rand"
	"reflect"
	"strings"

	sc "github.com/robskie/dynami/schema"

	db "github.com/aws/aws-sdk-go/service/dynamodb"
	dbattribute "github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
)

type indexType string

const (
	globalIndexType indexType = "GLOBAL"
	localIndexType  indexType = "LOCAL"
)

type (
	dbkey struct {
		value dbitem

		// indexName is the index name for this key.
		// If this is empty, then this key represents
		// the primary key.
		indexName string
		indexType indexType
	}

	dbitem map[string]*db.AttributeValue
)

// getKey returns a key from a value. This is done by
// first checking the primary key in val and returns it
// if it's nonempty. If it's empty, it will return the
// first nonempty secondary key.
func getKey(item interface{}) (*dbkey, error) {
	k, err := getPrimaryKey(item)
	if err != nil {
		k, err = getSecondaryKey(item)
	}

	return k, err
}

func getPrimaryKey(item interface{}) (*dbkey, error) {
	val := reflect.Indirect(reflect.ValueOf(item))
	item = val.Interface()

	kv, err := dbattribute.MarshalMap(item)
	if err != nil {
		return nil, fmt.Errorf("dynami: invalid item (%v)", err)
	}

	schema := sc.GetSchema(item)
	key := &dbkey{value: dbitem{}}
	for _, k := range schema.Key {
		v, err := valueByName(val, k.Name)
		if err != nil {
			return nil, fmt.Errorf("dynami: key (%v) has no value", k.Name)
		}

		if isZeroValue(v) {
			return nil, fmt.Errorf("dynami: incomplete primary key")
		}
		key.value[k.Name] = kv[k.Name]
	}

	if len(key.value) == 0 {
		return nil, fmt.Errorf("dynami: no valid key")
	}

	return key, nil
}

func getSecondaryKey(item interface{}) (*dbkey, error) {
	val := reflect.Indirect(reflect.ValueOf(item))
	item = val.Interface()

	kv, err := dbattribute.MarshalMap(item)
	if err != nil {
		return nil, fmt.Errorf("dynami: invalid item (%v)", err)
	}

	key := &dbkey{value: dbitem{}}
	schema := sc.GetSchema(item)

	globalIdxMarker := len(schema.LocalSecondaryIndexes)
	secondaryIdxs := make([]sc.SecondaryIndex, len(schema.LocalSecondaryIndexes))
	copy(secondaryIdxs, schema.LocalSecondaryIndexes)
	secondaryIdxs = append(secondaryIdxs, schema.GlobalSecondaryIndexes...)

Indices:
	// Get secondary indices
	for i, idx := range secondaryIdxs {
		for _, k := range idx.Key {
			v := val.FieldByName(k.Name)
			if isZeroValue(v) {
				key.value = dbitem{}
				continue Indices
			}

			key.value[k.Name] = kv[k.Name]
		}

		key.indexName = idx.Name
		key.indexType = localIndexType
		if i >= globalIdxMarker {
			key.indexType = globalIndexType
		}

		break
	}

	if len(key.value) == 0 {
		return nil, fmt.Errorf("dynami: no valid key")
	}

	return key, nil
}

func removeEmptyAttr(item dbitem) dbitem {
	for attrName, attrValue := range item {
		if attrValue.S != nil && *attrValue.S == "" {
			delete(item, attrName)
		} else if attrValue.NULL != nil && *attrValue.NULL == true {
			delete(item, attrName)
		} else if attrValue.M != nil {
			removeEmptyAttr(attrValue.M)
		}
	}

	return item
}

func toPtr(v interface{}) interface{} {
	switch vv := v.(type) {
	case string:
		if vv != "" {
			return &vv
		}
		return (*string)(nil)

	case bool:
		return &vv

	case uint, uint8, uint16, uint32, uint64,
		int, int8, int16, int32, int64:

		rv := reflect.ValueOf(vv)
		ret := reflect.New(reflect.PtrTo(rv.Type())).Elem()
		if rv.Int() >= 0 {
			pv := reflect.New(rv.Type())
			pv.Elem().Set(rv)
			ret.Set(pv)
		}
		return ret.Interface()
	default:
		panic("dynami: cannot convert type to pointer")
	}
}

func isZeroValue(val reflect.Value) bool {
	return !val.IsValid() || val.Interface() == reflect.Zero(val.Type()).Interface()
}

func valueByName(val reflect.Value, name string) (reflect.Value, error) {
	v := reflect.Value{}
	if val.Kind() == reflect.Struct {
		v = val.FieldByName(name)
		if !v.IsValid() {
			v = fieldByNameTag(val, name)
		}
	} else if val.Kind() == reflect.Map {
		v = val.MapIndex(reflect.ValueOf(name))
	}

	if !v.IsValid() {
		return v, fmt.Errorf("dynami: cannot find value with name (%v)", name)
	}
	return v, nil
}

func fieldByNameTag(val reflect.Value, name string) reflect.Value {
	t := val.Type()
	nf := t.NumField()
	for i := 0; i < nf; i++ {
		f := t.Field(i)

		// Consider only exported fields
		if f.PkgPath != "" {
			continue
		}

		// Get name from dynamodbav or json tag
		nameTag := f.Tag.Get("dynamodbav")
		if nameTag == "" {
			nameTag = f.Tag.Get("json")
		}

		tags := strings.Split(nameTag, ",")
		if len(tags) > 0 && tags[0] == name {
			return val.FieldByName(f.Name)
		}
	}

	return reflect.Value{}
}

func checkType(item interface{}, types ...interface{}) error {
	t := reflect.TypeOf(item)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	for _, validType := range types {
		switch vt := validType.(type) {
		case reflect.Kind:
			if t.Kind() == vt {
				return nil
			}
		default:
			if t == reflect.TypeOf(validType) {
				return nil
			}
		}
	}

	return fmt.Errorf("dynami: invalid type (%v)", t)
}

func checkPtrType(item interface{}, types ...interface{}) error {
	t := reflect.TypeOf(item)
	if t.Kind() != reflect.Ptr {
		return fmt.Errorf("dynami: invalid type (%v)", t)
	}

	t = t.Elem()
	for _, validType := range types {
		switch vt := validType.(type) {
		case reflect.Kind:
			if t.Kind() == vt {
				return nil
			}
		default:
			if t == reflect.TypeOf(validType) {
				return nil
			}
		}
	}

	return fmt.Errorf("dynami: invalid type (%v)", reflect.TypeOf(item))
}

func checkSliceType(item interface{}, types ...interface{}) error {
	t := reflect.TypeOf(item)
	if t.Kind() != reflect.Slice {
		return fmt.Errorf("dynami: invalid type (%v)", t)
	}

	t = t.Elem()
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	for _, validType := range types {
		switch vt := validType.(type) {
		case reflect.Kind:
			if t.Kind() == vt {
				return nil
			}
		default:
			if t == reflect.TypeOf(validType) {
				return nil
			}
		}
	}

	return fmt.Errorf("dynami: invalid type (%v)", reflect.TypeOf(item))
}

func max(a, b int) int {
	if a > b {
		return a
	}

	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}

	return b
}

func randString(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}
