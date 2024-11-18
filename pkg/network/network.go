// -*- Mode: Go; indent-tabs-mode: t -*-
/*
 * This file is part of AMS SDK
 * Copyright 2021 Canonical Ltd.
 *
 * This program is free software: you can redistribute it and/or modify it under
 * the terms of the Lesser GNU General Public License version 3, as published
 * by the Free Software Foundation.
 *
 * This program is distributed in the hope that it will be useful, but WITHOUT
 * ANY WARRANTY; without even the implied warranties of MERCHANTABILITY, SATISFACTORY
 * QUALITY, or FITNESS FOR A PARTICULAR PURPOSE.  See the Lesser GNU General Public
 * License for more details.
 *
 * You should have received a copy of the Lesser GNU General Public License along
 * with this program.  If not, see <http://www.gnu.org/licenses/>.
 */

package network

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/gorilla/websocket"
)

// RFC3493Dialer dialer
func RFC3493Dialer(network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}

	addrs, err := net.LookupHost(host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		c, err := net.DialTimeout(network, net.JoinHostPort(a, port), 10*time.Second)
		if err != nil {
			continue
		}
		return c, err
	}
	return nil, fmt.Errorf("Unable to connect to: " + address)
}

// InitTLSConfig returns a tls.Config populated with TLS1.3
// as the minimum TLS version for TLS configuration
func InitTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion: tls.VersionTLS13,
	}
}

func finalizeTLSConfig(tlsConfig *tls.Config, tlsRemoteCert *x509.Certificate) {
	// Trusted certificates
	if tlsRemoteCert != nil {
		caCertPool := tlsConfig.RootCAs
		if caCertPool == nil {
			caCertPool = x509.NewCertPool()
		}

		// Make it a valid RootCA
		tlsRemoteCert.IsCA = true
		tlsRemoteCert.KeyUsage = x509.KeyUsageCertSign

		// Setup the pool
		caCertPool.AddCert(tlsRemoteCert)
		tlsConfig.RootCAs = caCertPool

		// Set the ServerName
		if tlsRemoteCert.DNSNames != nil {
			tlsConfig.ServerName = tlsRemoteCert.DNSNames[0]
		}
	}
}

// GetTLSConfig returns a tls 1.2 config
func GetTLSConfig(tlsClientCertFile, tlsClientKeyFile, tlsClientCAFile string, tlsRemoteCert *x509.Certificate) (*tls.Config, error) {
	tlsConfig := InitTLSConfig()
	return getTLSConfig(tlsClientCertFile, tlsClientKeyFile, tlsClientCAFile, tlsRemoteCert, tlsConfig)
}

func getTLSConfig(tlsClientCertFile, tlsClientKeyFile, tlsClientCAFile string, tlsRemoteCert *x509.Certificate, tlsConfig *tls.Config) (*tls.Config, error) {
	// Client authentication
	if tlsClientCertFile != "" && tlsClientKeyFile != "" {
		cert, err := tls.LoadX509KeyPair(tlsClientCertFile, tlsClientKeyFile)
		if err != nil {
			return nil, err
		}

		tlsConfig.Certificates = []tls.Certificate{cert}
	}

	if tlsClientCAFile != "" {
		caCertificates, err := os.ReadFile(tlsClientCAFile)
		if err != nil {
			return nil, err
		}

		caPool := x509.NewCertPool()
		caPool.AppendCertsFromPEM(caCertificates)

		tlsConfig.RootCAs = caPool
	}

	finalizeTLSConfig(tlsConfig, tlsRemoteCert)
	return tlsConfig, nil
}

// ListAvailableAddresses returns a list of IPv4 network addresses the host has.
// It ignores the loopback device
func ListAvailableAddresses() ([]string, error) {
	ret := []string{}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil, err
	}

	for _, addr := range addrs {
		if ipnet, ok := addr.(*net.IPNet); ok && !(ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast()) {
			if ipnet.IP.To4() != nil {
				ret = append(ret, ipnet.IP.String())
			}
		}
	}

	return ret, nil
}

// GetLocalIP returns the first non loopback address of the system we're running on
func GetLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	for _, address := range addrs {
		// check the address type and if it is not a loopback the display it
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return ""
}

// WebsocketSendStream manages the send stream of the websocket
func WebsocketSendStream(conn *websocket.Conn, r io.Reader, bufferSize int) chan bool {
	return WebsocketSendStreamWithContext(context.Background(), conn, r, bufferSize)
}

// WebsocketSendStreamWithContext manages the send stream of the websocket
func WebsocketSendStreamWithContext(ctx context.Context, conn *websocket.Conn, r io.Reader, bufferSize int) chan bool {
	ch := make(chan bool)

	if r == nil {
		close(ch)
		return ch
	}

	go func(ctx context.Context, conn *websocket.Conn, r io.Reader) {
		in := ReaderToChannel(r, bufferSize)
		active := true
		for active {
			select {
			case buf, ok := <-in:
				if !ok {

					active = false
					break
				}

				w, err := conn.NextWriter(websocket.BinaryMessage)
				if err != nil {
					log.Printf("Got error getting next writer %s", err)
					active = false
					break
				}

				_, err = w.Write(buf)
				w.Close()
				if err != nil {
					log.Printf("Got err writing %s", err)
				}
			case <-ctx.Done():
				active = false
			}
		}
		ch <- true
	}(ctx, conn, r)

	return ch
}

// WebsocketRecvStream manages the recv stream of the socket
func WebsocketRecvStream(w io.Writer, conn *websocket.Conn) chan bool {
	ch := make(chan bool)

	go func(w io.Writer, conn *websocket.Conn) {
		for {
			mt, r, err := conn.NextReader()
			if err != nil {
				switch err.(type) {
				case *websocket.CloseError:
				default:
				}
				break
			}

			if mt == websocket.CloseMessage {
				log.Printf("Got close message for reader")
				break
			}

			if mt == websocket.TextMessage {
				log.Printf("got message barrier (type %d)", mt)
				break
			}

			buf, err := io.ReadAll(r)
			if err != nil {
				log.Printf("Got error writing to writer %s", err)
				break
			}

			if w == nil {
				continue
			}

			i, err := w.Write(buf)
			if i != len(buf) {
				log.Printf("Didn't write all of buf")
				break
			}
			if err != nil {
				log.Printf("Error writing buf %s", err)
				break
			}
		}
		ch <- true
	}(w, conn)

	return ch
}

// WebsocketProxy proxies a websocket connection
func WebsocketProxy(source *websocket.Conn, target *websocket.Conn) chan bool {
	forward := func(in *websocket.Conn, out *websocket.Conn, ch chan bool) {
		for {
			mt, r, err := in.NextReader()
			if err != nil {
				break
			}

			w, err := out.NextWriter(mt)
			if err != nil {
				break
			}

			_, err = io.Copy(w, r)
			w.Close()
			if err != nil {
				break
			}
		}

		ch <- true
	}

	chSend := make(chan bool)
	go forward(source, target, chSend)

	chRecv := make(chan bool)
	go forward(target, source, chRecv)

	ch := make(chan bool)
	go func() {
		select {
		case <-chSend:
		case <-chRecv:
		}

		source.Close()
		target.Close()

		ch <- true
	}()

	return ch
}

func defaultReader(conn *websocket.Conn, r io.ReadCloser, readDone chan<- bool) {
	/* For now, we don't need to adjust buffer sizes in
	* WebsocketMirror, since it's used for interactive things like
	* exec.
	 */
	in := ReaderToChannel(r, -1)
	for {
		buf, ok := <-in
		if !ok {
			r.Close()
			log.Printf("sending write barrier")
			conn.WriteMessage(websocket.TextMessage, []byte{})
			readDone <- true
			return
		}
		w, err := conn.NextWriter(websocket.BinaryMessage)
		if err != nil {
			log.Printf("Got error getting next writer %s", err)
			break
		}

		_, err = w.Write(buf)
		w.Close()
		if err != nil {
			log.Printf("Got err writing %s", err)
			break
		}
	}
	closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
	conn.WriteMessage(websocket.CloseMessage, closeMsg)
	readDone <- true
	r.Close()
}

// ReaderToChannel reads from websocket and sends to a channel
func ReaderToChannel(r io.Reader, bufferSize int) <-chan []byte {
	if bufferSize <= 128*1024 {
		bufferSize = 128 * 1024
	}

	ch := make(chan ([]byte))

	go func() {
		readSize := 128 * 1024
		offset := 0
		buf := make([]byte, bufferSize)

		for {
			read := buf[offset : offset+readSize]
			nr, err := r.Read(read)
			if err != nil {
				close(ch)
				break
			}

			offset += nr
			if offset > 0 && (offset+readSize >= bufferSize) {
				ch <- buf[0:offset]
				offset = 0
				buf = make([]byte, bufferSize)
			}
		}
	}()

	return ch
}

func defaultWriter(conn *websocket.Conn, w io.WriteCloser, writeDone chan<- bool) {
	for {
		mt, r, err := conn.NextReader()
		if err != nil {
			log.Printf("Got error getting next reader %s, %s", err, w)
			break
		}

		if mt == websocket.CloseMessage {
			log.Printf("Got close message for reader")
			break
		}

		if mt == websocket.TextMessage {
			log.Printf("Got message barrier, resetting stream")
			break
		}

		buf, err := io.ReadAll(r)
		if err != nil {
			log.Printf("Got error writing to writer %s", err)
			break
		}
		i, err := w.Write(buf)
		if i != len(buf) {
			log.Printf("Didn't write all of buf")
			break
		}
		if err != nil {
			log.Printf("Error writing buf %s", err)
			break
		}
	}
	writeDone <- true
	w.Close()
}

// WebSocketMirrorReader mirror reader
type WebSocketMirrorReader func(conn *websocket.Conn, r io.ReadCloser, readDone chan<- bool)

// WebSocketMirrorWriter mirror writer
type WebSocketMirrorWriter func(conn *websocket.Conn, w io.WriteCloser, writeDone chan<- bool)

// WebsocketMirror allows mirroring a reader to a websocket and taking the
// result and writing it to a writer. This function allows for multiple
// mirrorings and correctly negotiates stream endings. However, it means any
// websocket.Conns passed to it are live when it returns, and must be closed
// explicitly.
func WebsocketMirror(conn *websocket.Conn, w io.WriteCloser, r io.ReadCloser, Reader WebSocketMirrorReader, Writer WebSocketMirrorWriter) (chan bool, chan bool) {
	readDone := make(chan bool, 1)
	writeDone := make(chan bool, 1)

	ReadFunc := Reader
	if ReadFunc == nil {
		ReadFunc = defaultReader
	}

	WriteFunc := Writer
	if WriteFunc == nil {
		WriteFunc = defaultWriter
	}

	go ReadFunc(conn, r, readDone)
	go WriteFunc(conn, w, writeDone)

	return readDone, writeDone
}

// WebsocketConsoleMirror console mirror
func WebsocketConsoleMirror(conn *websocket.Conn, w io.WriteCloser, r io.ReadCloser) (chan bool, chan bool) {
	readDone := make(chan bool, 1)
	writeDone := make(chan bool, 1)

	go defaultWriter(conn, w, writeDone)

	go func(conn *websocket.Conn, r io.ReadCloser) {
		in := ReaderToChannel(r, -1)
		for {
			buf, ok := <-in
			if !ok {
				r.Close()
				log.Printf("sending write barrier")
				conn.WriteMessage(websocket.TextMessage, []byte{})
				readDone <- true
				return
			}
			w, err := conn.NextWriter(websocket.BinaryMessage)
			if err != nil {
				log.Printf("Got error getting next writer %s", err)
				break
			}

			_, err = w.Write(buf)
			w.Close()
			if err != nil {
				log.Printf("Got err writing %s", err)
				break
			}
		}
		closeMsg := websocket.FormatCloseMessage(websocket.CloseNormalClosure, "")
		conn.WriteMessage(websocket.CloseMessage, closeMsg)
		readDone <- true
		r.Close()
	}(conn, r)

	return readDone, writeDone
}

// WebsocketUpgrader websocket.Upgrader
var WebsocketUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}
