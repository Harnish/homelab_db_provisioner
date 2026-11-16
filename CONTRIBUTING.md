# Contributing

Bug reports and pull requests are welcome.

## Running the tests

```bash
go vet ./...
go test ./...
```

Integration tests run against real servers and are skipped unless these are set:

```bash
export MIGRATE_SOURCE_CONN='postgres://postgres:rootpassword@localhost:5432/postgres?sslmode=disable'
export MIGRATE_TARGET_CONN='postgres://postgres:rootpassword@localhost:5433/postgres?sslmode=disable'
export TEST_MARIADB_CONN='mariadb://root:rootpassword@localhost:3306/'
export TEST_MONGO_CONN='mongodb://admin:rootpassword@127.0.0.1:27017/admin'
go test -tags integration ./...
```

`docker-compose up postgres postgres-target mariadb` starts the first three. The tests need `pg_dump` and `psql` on your PATH, at least as new as the newest PostgreSQL server; the MongoDB backup test also needs `mongodump`.

## Pull requests

- Add a test that fails without your change.
- Use conventional commit messages (`fix:`, `feat:`, `chore:`).
- Never log or print a password, including in dry-run output and tests.

## Security issues

Please report vulnerabilities privately through GitHub's "Report a vulnerability" button on the repository's Security tab, not in a public issue.
