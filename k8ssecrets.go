package main

import "fmt"

func secretNameFor(serverName, database string) string {
	return fmt.Sprintf("%s-%s-credentials", slugify(serverName), slugify(database))
}
