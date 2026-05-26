package api

// base.yaml 是 OpenAPI 根文档，具体 paths/components 在 ../../api/openapi/ 子目录。
// 生成前先将多文件规范打包为单文件，避免 oapi-codegen 对跨文件同包引用要求 import-mapping。
//go:generate go run ../tools/openapi-bundle -in ../../api/openapi/base.yaml -out ../../api/openapi/openapi-3.0.yaml
//go:generate go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen -generate chi-server,types,spec -package api -o openapi.gen.go ../../api/openapi/openapi-3.0.yaml
