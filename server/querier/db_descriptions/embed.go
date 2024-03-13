package db_descriptions

import "embed"

//go:embed clickhouse/tag/enum/*
var EnumFiles embed.FS
