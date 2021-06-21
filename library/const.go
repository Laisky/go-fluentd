package library

const (
	DefaultFieldForMessage = "message"
)

var (
	MustIncludeFileds = []string{
		"tag",
		"@timestamp",
		"msgid",
		"container_id",
		"container_name",
		"level",
		"datasource",
	}
)
