/*
grokstat is a tool for querying game servers for various information: server list, player count, active map etc

The program takes protocol name and remote ip address as arguments, fetches information from the remote server, parses it and outputs back as JSON. As convenience the status and message are also provided.

Usage of grokstat utility:
	-ip string
		IP address of server to query.
	-protocol string
		Server protocol to use.
*/
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/grokstat/grokstat/protocols"
)

// A convenience function for creating UDP connections
func newUDPConnection(addr string, protocol string) (*net.UDPConn, error) {
	raddr, _ := net.ResolveUDPAddr("udp", addr)
	caddr, _ := net.ResolveUDPAddr("udp", ":0")
	conn, err := net.DialUDP(protocol, caddr, raddr)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

// A convenience function for creating TCP connections
func newTCPConnection(addr string, protocol string) (*net.TCPConn, error) {
	raddr, _ := net.ResolveTCPAddr("tcp", addr)
	caddr, _ := net.ResolveTCPAddr("tcp", ":0")
	conn, err := net.DialTCP(protocol, caddr, raddr)
	if err != nil {
		return nil, err
	}
	return conn, nil
}

func connect_send_receive(protocol string, addr string, request []byte) ([]byte, error) {
	var status []byte
	var err error
	emptyResponse := errors.New("No response from server")

	if protocol == "tcp" {
		conn, connection_err := newTCPConnection(addr, protocol)
		if connection_err != nil {
			return []byte{}, connection_err
		}
		defer conn.Close()
		var buf string
		buf, err = bufio.NewReader(conn).ReadString('\n')
		status = []byte(buf)
	} else if protocol == "udp" {
		conn, connection_err := newUDPConnection(addr, protocol)
		if connection_err != nil {
			return []byte{}, connection_err
		}
		defer conn.Close()
		conn.Write(request)
		buf_len := 65535
		buf := make([]byte, buf_len)
		conn.SetDeadline(time.Now().Add(time.Duration(5) * time.Second))
		conn.ReadFromUDP(buf)
		if err != nil {
			return []byte{}, err
		} else {
			status = bytes.TrimRight(buf, "\x00")
			if len(status) == 0 {
				err = emptyResponse
			}
		}
	}
	return status, err
}

func ParseScheme(protocol_string string) string {
	var protocol string

	if protocol_string == "udp" {
		protocol = "udp"
	} else {
		protocol = "tcp"
	}

	return protocol
}

func ParseIPAddr(ipString string, defaultPort string) map[string]string {
	urlInfo, _ := url.Parse(ipString)

	result := make(map[string]string)
	result["http_protocol"] = ParseScheme(urlInfo.Scheme)
	result["host"] = urlInfo.Host

	return result
}

// Forms a JSON string out of server list.
func FormJsonString(output_field string, output interface{}, err error) (string, error) {
	result := make(map[string]interface{})
	if err != nil {
		result["servers"] = []string{}
		result["status"] = 500
		result["message"] = err.Error()
	} else {
		result[output_field] = output
		result["status"] = 200
		result["message"] = "OK"
	}

	jsonOut, jsonErr := json.Marshal(result)

	if jsonErr != nil {
		jsonOut = []byte(`{}`)
	}

	return string(jsonOut), jsonErr
}

func main() {
	var remoteIp string
	var protocolFlag string
	var showProtocolsFlag bool
	flag.StringVar(&remoteIp, "ip", "", "IP address of server to query.")
	flag.StringVar(&protocolFlag, "protocol", "", "Server protocol to use.")
	flag.BoolVar(&showProtocolsFlag, "showProtocols", false, "Output available server protocols.")
	flag.Parse()

	protocolCmdMap := protocols.MakeProtocolMap()

	if flag.NFlag() == 0 {flag.PrintDefaults();return}

	if showProtocolsFlag {
		outputMapProtocols := make(map[string]interface{})
		for _, v := range protocolCmdMap {
			outputMapProtocols[v.Information.Id] = v.Information
		}

		jsonOut, _ := FormJsonString("protocols", outputMapProtocols, nil)

		fmt.Println(string(jsonOut))

		return
	}

	var resultErr error

	if remoteIp == "" {
		resultErr = errors.New("Please specify a valid IP.")
	}
	if protocolFlag == "" {
		resultErr = errors.New("Please specify the protocol.")
	}

	var protocol protocols.ProtocolEntry
	if resultErr == nil {
		var g_ok bool
		protocol, g_ok = protocolCmdMap[protocolFlag]
		if g_ok == false {
			resultErr = errors.New("Invalid protocol specified.")
		}
	}

	var response []byte
	if resultErr == nil {
		var responseErr error
		ipMap := ParseIPAddr(remoteIp, protocol.Information.DefaultRequestPort)
		response, responseErr = connect_send_receive(ipMap["http_protocol"], ipMap["host"], []byte(protocol.RequestPrelude))
		resultErr = responseErr
	}

	var servers []string
	if resultErr == nil {
		var responseParseErr error
		servers, responseParseErr = protocol.Information.ResponseParseFunc([]byte(response), []byte(protocol.ResponsePrelude))
		resultErr = responseParseErr
	}

	jsonOut, _ := FormJsonString("servers", servers, resultErr)

	fmt.Println(jsonOut)
}
