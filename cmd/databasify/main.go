package main

import (
	"os"

	mysql "github.com/databasifylabs/engine-mysql/engine"
	sdk "github.com/databasifylabs/engine-sdk"
)

func main() {
	os.Exit(sdk.Run(mysql.New()))
}
