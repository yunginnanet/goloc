# goloc

Simple string extraction tool for translation purposes

### Fork

Rewrite for the modern era in progress;

  - code cleanup
  - improve performance by avoiding slice iteration to check for matching values (prefer maps)
  - attempt to modernize usage of `go/ast` package
  - swap zap logger for zerolog
  - turn test into a proper golang unit test
  - restructure directories
  - add `Makefile`
