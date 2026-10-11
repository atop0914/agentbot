package storage

import "embed"

// migrationFS 内嵌 migrations/ 下的全部 .sql 文件。
//
// 内嵌而非运行时读目录：单二进制交付时不需要额外挂载迁移目录，
// 也不会出现「代码是新的、迁移目录还是旧的」这种错配。
// 代价是改迁移必须重新编译 —— 迁移本来就随版本发布，可以接受。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

// migrationFiles 是「版本号 -> SQL 正文」的映射，在包初始化时从内嵌文件读入。
//
// 读失败（例如 embed 指令与实际目录不一致）会让进程在启动阶段直接 panic：
// 这属于编译期就该发现的问题，让它带着明确信息炸在最前面，
// 比跑到 Migrate 时才报一个「文件不存在」好得多。
var migrationFiles = loadEmbeddedMigrations()
