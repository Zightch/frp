package testhooks

type Field struct {
	Key   string
	Value any
}

func F(key string, value any) Field {
	return Field{Key: key, Value: value}
}

type Hit struct {
	Point  string
	Index  int
	Fields map[string]any
}
