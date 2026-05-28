package api

// OpenAPI 多文件规范由仓库根任务 openapi:bundle 合并为 ../../../api/openapi/openapi-3.0.yaml。
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -generate chi-server,types,spec -package api -o openapi.gen.go ../../../api/openapi/openapi-3.0.yaml
