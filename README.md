# fcgirt: FastCGI http.RoundTripper

## Example usage

```Go
package main

import (
	"bytes"
	"fmt"
	"github.com/johto/fcgirt"
	"net"
	"net/http"
)

func main() {
	dialer := fcgirt.DialerFunc(func () (net.Conn, error) {
		return net.Dial("unix", "/opt/api_socket")
	})
	fcgiRoundTripper := fcgirt.NewRoundTripper(dialer)
	client := &http.Client{
		Transport: fcgiRoundTripper,
	}

	postData := bytes.NewBufferString(`{"method": "Hello"}`)
	req, err := http.NewRequest("POST", "http://127.0.0.1/api/Legacy", postData)
	if err != nil {
		panic(err)
	}
	response, err := client.Do(req)
	if err != nil {
		panic(err)
	}
	fmt.Println(response)
}
```
