/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package netcup

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// soapEndpoint is the netcup vServer SOAP service URL (without ?wsdl).
const soapEndpoint = "https://www.servercontrolpanel.de/WSEndUser"

// soapNS is the target namespace declared in the WSDL.
// Verify against https://www.servercontrolpanel.de/WSEndUser?wsdl if requests fail.
const soapNS = "http://enduser.provider.netcup.de/"

// Client calls the two netcup SOAP methods used for failover IP routing.
type Client struct {
	endpoint string
	login    string
	password string
	http     *http.Client
}

func New(login, password string) *Client {
	return &Client{
		endpoint: soapEndpoint,
		login:    login,
		password: password,
		http:     &http.Client{},
	}
}

// GetVServerIPs returns the IP addresses currently routed to the given vserver.
func (c *Client) GetVServerIPs(ctx context.Context, vserverName string) ([]string, error) {
	body := fmt.Sprintf(
		`<ns:getVServerIPs xmlns:ns=%q><loginName>%s</loginName><password>%s</password><vserverName>%s</vserverName></ns:getVServerIPs>`,
		soapNS, xmlEscape(c.login), xmlEscape(c.password), xmlEscape(vserverName),
	)
	resp, err := c.call(ctx, body)
	if err != nil {
		return nil, err
	}

	var env struct {
		Body struct {
			Response struct {
				Returns []string `xml:"return"`
			} `xml:"getVServerIPsResponse"`
			Fault *soapFault `xml:"Fault"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(resp, &env); err != nil {
		return nil, fmt.Errorf("parsing getVServerIPs response: %w", err)
	}
	if env.Body.Fault != nil {
		return nil, fmt.Errorf("SOAP fault: %s", env.Body.Fault.FaultString)
	}
	return env.Body.Response.Returns, nil
}

// ChangeIPRouting reroutes ip/mask to the vserver identified by vserverName and mac.
func (c *Client) ChangeIPRouting(ctx context.Context, ip string, mask int, vserverName, mac string) error {
	body := fmt.Sprintf(
		`<ns:changeIPRouting xmlns:ns=%q>`+
			`<loginName>%s</loginName>`+
			`<password>%s</password>`+
			`<routedIP>%s</routedIP>`+
			`<routedMask>%d</routedMask>`+
			`<destinationVserverName>%s</destinationVserverName>`+
			`<destinationInterfaceMAC>%s</destinationInterfaceMAC>`+
			`</ns:changeIPRouting>`,
		soapNS,
		xmlEscape(c.login), xmlEscape(c.password),
		xmlEscape(ip), mask,
		xmlEscape(vserverName), xmlEscape(mac),
	)
	resp, err := c.call(ctx, body)
	if err != nil {
		return err
	}

	var env struct {
		Body struct {
			Fault *soapFault `xml:"Fault"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal(resp, &env); err != nil {
		return fmt.Errorf("parsing changeIPRouting response: %w", err)
	}
	if env.Body.Fault != nil {
		return fmt.Errorf("SOAP fault: %s", env.Body.Fault.FaultString)
	}
	return nil
}

type soapFault struct {
	FaultCode   string `xml:"faultcode"`
	FaultString string `xml:"faultstring"`
}

func (c *Client) call(ctx context.Context, bodyContent string) ([]byte, error) {
	envelope := `<?xml version="1.0" encoding="UTF-8"?>` +
		`<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/">` +
		`<soapenv:Header/>` +
		`<soapenv:Body>` + bodyContent + `</soapenv:Body>` +
		`</soapenv:Envelope>`

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, strings.NewReader(envelope))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=UTF-8")
	req.Header.Set("SOAPAction", "")

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("netcup SOAP call: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	data, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, fmt.Errorf("reading netcup response: %w", err)
	}
	if res.StatusCode/100 != 2 {
		return nil, fmt.Errorf("netcup returned HTTP %d: %s", res.StatusCode, data)
	}
	return data, nil
}

func xmlEscape(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}
