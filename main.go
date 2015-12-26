/*
grokstat is a tool for querying game servers for various information: server list, player count, active map etc

The program takes protocol name and remote ip address as arguments, fetches information from the remote server, parses it and outputs back as JSON. As convenience the status and message are also provided.

grokstat uses JSON input instead of command line flags. The JSON input is structured as follows:
	hosts - array of strings - hosts to query
	protocol - string - protocol to use
	show-protocols - boolean - if true, show protocols and exit
	custom-config-path - path of custom config file to be used
*/
package main

//go:generate go-bindata -o "bindata/bindata.go" -pkg "bindata" "data/..."

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/grokstat/grokstat/bindata"
	"github.com/grokstat/grokstat/models"
	"github.com/grokstat/grokstat/protocols"
	"github.com/grokstat/grokstat/util"
)

type InputData struct {
	Hosts            []string `json:"hosts"`
	Protocol         string   `json:"protocol"`
	ShowProtocols    bool     `json:"show-protocols"`
	CustomConfigPath string   `json:"custom-config-path"`
}

type ConfigFile struct {
	Protocols []protocols.ProtocolConfig `toml:"Protocols"`
}

type JsonResponse struct {
	Version string      `json:"version"`
	Status  int         `json:"status"`
	Message string      `json:"message"`
	Flags   InputData   `json:"input-flags"`
	Output  interface{} `json:"output"`
}

// Forms a JSON string out of Grokstat output.
func FormJsonResponse(output interface{}, err error, flags InputData) (string, error) {
	result := JsonResponse{Version: VERSION, Flags: flags}

	if err != nil {
		result.Output = `{}`
		result.Status = 500
		result.Message = err.Error()
	} else {
		result.Output = output
		result.Status = 200
		result.Message = "OK"
	}

	jsonOut, jsonErr := json.Marshal(result)

	if jsonErr != nil {
		jsonOut = []byte(`{}`)
	}

	return string(jsonOut), jsonErr
}

var DefaultConfigBinPath string = "data/grokstat.toml"

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

func QueryServer(httpProtocol string, addr string, request []byte) ([]byte, error) {
	var status []byte
	var err error
	emptyResponse := errors.New("No response from server")

	if httpProtocol == "tcp" {
		conn, connection_err := newTCPConnection(addr, httpProtocol)
		if connection_err != nil {
			return []byte{}, connection_err
		}
		defer conn.Close()
		var buf string
		buf, err = bufio.NewReader(conn).ReadString('\n')
		status = []byte(buf)
	} else if httpProtocol == "udp" {
		conn, connection_err := newUDPConnection(addr, httpProtocol)
		if connection_err != nil {
			return []byte{}, connection_err
		}
		defer conn.Close()
		conn.Write(request)
		buf_len := 16777215
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

func ParseIPAddr(ipString string, defaultPort string) map[string]string {
	var ipStringMod string

	if len(strings.Split(ipString, "://")) == 1 {
		ipStringMod = "placeholder://" + ipString
	} else {
		ipStringMod = ipString
	}

	urlInfo, _ := url.Parse(ipStringMod)

	result := make(map[string]string)
	result["http_protocol"] = urlInfo.Scheme
	result["host"] = urlInfo.Host

	if len(strings.Split(result["host"], ":")) == 1 {
		result["host"] = result["host"] + ":" + defaultPort
	}

	return result
}

func PrintProtocols(protocolCmdMap map[string]models.ProtocolEntry, flags InputData) {
	var outputMapProtocols []models.ProtocolEntryInfo
	for _, v := range protocolCmdMap {
		outputMapProtocols = append(outputMapProtocols, v.Information)
	}

	output := make(map[string]interface{})
	output["protocols"] = outputMapProtocols

	PrintJsonResponse(output, nil, flags)
}

func PrintError(err error, flags InputData) {
	PrintJsonResponse(nil, err, flags)
}

func PrintJsonResponse(output interface{}, err error, flags InputData) {
	jsonOut, _ := FormJsonResponse(output, err, flags)
	fmt.Println(jsonOut)
}

func Query(selectedProtocol string, protocolCmdMap map[string]models.ProtocolEntry, hosts []string) (output []models.ServerEntry, err error) {
	output = []models.ServerEntry{}
	var wg sync.WaitGroup
	var selectedQueryProtocol string
	var queryProtocol models.ProtocolEntry
	var serverHosts []string

	protocol, p_ok := protocolCmdMap[selectedProtocol]
	if p_ok == false {
		return []models.ServerEntry{}, errors.New("Invalid protocol specified.")
	}
	if protocol.Base.IsMaster {
		var q_ok bool
		selectedQueryProtocol, q_ok = protocol.Information["MasterOf"]
		queryProtocol, q_ok = protocolCmdMap[selectedQueryProtocol]
		if q_ok == false {
			return []models.ServerEntry{}, errors.New("Invalid query part attached to master protocol.")
		}

		var serverList []string
		for _, masterIp := range hosts {
			if masterIp == "" {
				continue
			}
			var response []byte
			var responseErr error
			ipMap := ParseIPAddr(masterIp, protocol.Information["DefaultRequestPort"])
			hostname := ipMap["host"]
			response, responseErr = QueryServer(protocol.Base.HttpProtocol, hostname, protocol.Base.MakeRequestPacketFunc(protocol.Information))
			if responseErr != nil {
				continue
			}

			data, responseParseErr := protocol.Base.MasterResponseParseFunc(response, protocol.Information)
			if responseParseErr != nil {
				continue
			}

			for _, dataEntry := range data {
				serverList = append(serverList, dataEntry)
			}
		}
		serverHosts = util.RemoveDuplicates(serverList)
	} else {
		serverHosts = hosts
		queryProtocol = protocol
	}

	wg.Add(len(serverHosts))
	dataChan := make(chan models.ServerEntry, 1)
	for _, remoteIp := range serverHosts {
		go func(remoteIp string, dataChan chan models.ServerEntry) {
			defer wg.Done()
			if remoteIp == "" {
				return
			}

			var response []byte
			var responseErr error
			ipMap := ParseIPAddr(remoteIp, queryProtocol.Information["DefaultRequestPort"])
			hostname := ipMap["host"]
			response, responseErr = QueryServer(queryProtocol.Base.HttpProtocol, hostname, queryProtocol.Base.MakeRequestPacketFunc(queryProtocol.Information))
			if responseErr != nil {
				return
			}

			serverEntry, responseParseErr := queryProtocol.Base.ServerResponseParseFunc(response, queryProtocol.Information)
			serverEntry.Host = hostname
			serverEntry.Protocol = selectedQueryProtocol

			if responseParseErr != nil {
				return
			}
			dataChan <- serverEntry
		}(remoteIp, dataChan)
	}

	go func() {
		defer wg.Done()
		for v := range dataChan {
			output = append(output, v)
		}
	}()

	wg.Wait()

	return output, err
}

func main() {
	var configInstance ConfigFile

	// Resets flags to default state, reads JSON from stdin
	reader := bufio.NewReader(os.Stdin)
	text, _ := reader.ReadString('\n')

	jsonFlags := InputData{Hosts: []string{}, Protocol: "", CustomConfigPath: "", ShowProtocols: false}
	jsonErr := json.Unmarshal([]byte(text), &jsonFlags)

	if jsonErr != nil {
		PrintError(jsonErr, jsonFlags)
		return
	}

	hosts := util.RemoveDuplicates(jsonFlags.Hosts)
	showProtocols := jsonFlags.ShowProtocols
	customConfigPath := jsonFlags.CustomConfigPath
	selectedProtocol := jsonFlags.Protocol

	if customConfigPath == "" {
		configBinData, err := bindata.Asset(DefaultConfigBinPath)
		if err != nil {
			PrintError(errors.New("Default config file not found."), jsonFlags)
			return
		}
		toml.Decode(string(configBinData), &configInstance)
	} else {
		_, err := toml.DecodeFile(customConfigPath, &configInstance)
		if err != nil {
			PrintError(errors.New("Error loading custom config file."), jsonFlags)
			return
		}
	}

	protocolCmdMap := protocols.MakeProtocolMap(configInstance.Protocols)

	if showProtocols {
		PrintProtocols(protocolCmdMap, jsonFlags)
		return
	}

	if selectedProtocol == "" {
		PrintError(errors.New("Please specify the protocol."), jsonFlags)
		return
	}
	if len(hosts) == 0 {
		PrintError(errors.New("No hosts specified."), jsonFlags)
		return
	}

	data, err := Query(selectedProtocol, protocolCmdMap, hosts)

	if err != nil {
		PrintError(err, jsonFlags)
		return
	} else {
		PrintJsonResponse(map[string]interface{}{"servers": data}, err, jsonFlags)
	}
}
