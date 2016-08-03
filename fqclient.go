package fq

/*
 * Copyright (c) 2016 Circonus, Inc.
 * All rights reserved.
 * 
 * Permission is hereby granted, free of charge, to any person obtaining a copy
 * of this software and associated documentation files (the "Software"), to
 * deal in the Software without restriction, including without limitation the
 * rights to use, copy, modify, merge, publish, distribute, sublicense, and/or
 * sell copies of the Software, and to permit persons to whom the Software is
 * furnished to do so, subject to the following conditions:
 * 
 * The above copyright notice and this permission notice shall be included in
 * all copies or substantial portions of the Software.
 * 
 * THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
 * IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
 * FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
 * AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
 * LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING
 * FROM, OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS
 * IN THE SOFTWARE.
 */

import (
	"encoding/binary"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/nu7hatch/gouuid"
)

func getNativeEndian() binary.ByteOrder {
	var i int32 = 0x01020304
	u := unsafe.Pointer(&i)
	pb := (*byte)(u)
	b := *pb
	if b == 0x04 {
		return binary.LittleEndian
	}
	return binary.BigEndian
}

var ne = getNativeEndian()
var be = binary.BigEndian
var rng = rand.New(rand.NewSource(time.Now().Unix() * int64(os.Getpid())))

type PeeringMode uint32

const (
	FQ_PROTO_CMD_MODE      = PeeringMode(0xcc50cafe)
	FQ_PROTO_DATA_MODE     = PeeringMode(0xcc50face)
	FQ_PROTO_PEER_MODE     = PeeringMode(0xcc50feed)
	FQ_PROTO_OLD_PEER_MODE = PeeringMode(0xcc50fade)
)
const (
	FQ_DEFAULT_QUEUE_TYPE = "mem"
	FQ_BIND_PEER          = uint16(0x00000001)
	FQ_BIND_PERM          = uint16(0x00000110)
	FQ_BIND_TRANS         = uint16(0x00000100)
	FQ_BIND_ILLEGAL       = uint32(0xffffffff)
)

type ProtoCommand uint16

const (
	FQ_PROTO_ERROR      = ProtoCommand(0xeeee)
	FQ_PROTO_AUTH_CMD   = ProtoCommand(0xaaaa)
	FQ_PROTO_AUTH_PLAIN = ProtoCommand(0)
	FQ_PROTO_AUTH_RESP  = ProtoCommand(0xaa00)
	FQ_PROTO_HBREQ      = ProtoCommand(0x4848)
	FQ_PROTO_HB         = ProtoCommand(0xbea7)
	FQ_PROTO_BINDREQ    = ProtoCommand(0xb170)
	FQ_PROTO_BIND       = ProtoCommand(0xb171)
	FQ_PROTO_UNBINDREQ  = ProtoCommand(0x071b)
	FQ_PROTO_UNBIND     = ProtoCommand(0x171b)
	FQ_PROTO_STATUS     = ProtoCommand(0x57a7)
	FQ_PROTO_STATUSREQ  = ProtoCommand(0xc7a7)

	FQ_MAX_RK_LEN = 127
	FQ_MAX_HOPS   = 32
)

type fq_rk struct {
	name [FQ_MAX_RK_LEN]byte
	len  uint8
}

func Rk(str string) fq_rk {
	input := []byte(str)
	inlen := len(input)
	if inlen > FQ_MAX_RK_LEN {
		inlen = FQ_MAX_RK_LEN
	}
	rk := fq_rk{}
	copy(rk.name[:], input[:inlen])
	rk.len = uint8(inlen)
	return rk
}

func (rk *fq_rk) ToString() string {
	return string(rk.name[:rk.len])
}

type fq_msgid struct {
	d [16]byte
}

type Message struct {
	Hops                    [FQ_MAX_HOPS]uint32
	Route, Sender, Exchange fq_rk
	Sender_msgid            fq_msgid
	Payload_len             uint32
	Arrival_time            uint64
	Payload                 []byte
}

func (id *fq_msgid) SetUint32(u1, u2 uint32) {
	ne.PutUint32(id.d[0:], u1)
	ne.PutUint32(id.d[4:], u2)
}
func (id *fq_msgid) SetUint64(u1 uint64) {
	ne.PutUint64(id.d[0:], u1)
}
func (id *fq_msgid) GetUint32() (uint32, uint32, uint32, uint32) {
	return ne.Uint32(id.d[0:]), ne.Uint32(id.d[4:]), ne.Uint32(id.d[8:]), ne.Uint32(id.d[12:])
}
func (id *fq_msgid) GetUint64() (uint64, uint64) {
	return ne.Uint64(id.d[0:]), ne.Uint64(id.d[8:])
}
func NewMessage(exchange, route string, payload []byte) *Message {
	msg := &Message{}
	msg.Exchange = Rk(exchange)
	msg.Route = Rk(route)
	if payload != nil {
		msg.Payload = payload
		msg.Payload_len = uint32(len(msg.Payload))
	}
	rand.Read(msg.Sender_msgid.d[0:8])
	return msg
}

type Hooks interface {
	AuthHook(c *Client, err error)
	BindHook(c *Client, req *BindReq)
	UnbindHook(c *Client, req *UnbindReq)
	MessageHook(c *Client, msg *Message) bool
	CleanupHook(c *Client)
	DisconnectHook(c *Client)
	ErrorLogHook(c *Client, error string)
}

type Userdata interface{}

type HookType int

const (
	AUTH_HOOK_TYPE = HookType(iota)
	CMD_HOOK_TYPE  = HookType(iota)
)

type BindReq struct {
	Exchange   fq_rk
	Flags      uint16
	Program    string
	OutRouteId uint32
}
type UnbindReq struct {
	Exchange   fq_rk
	RouteId    uint32
	OutSuccess uint32
}

type statusvalue struct {
	Key   string
	Value uint32
}

type fq_cmd_instr struct {
	cmd  ProtoCommand
	data struct {
		heartbeat struct {
			interval time.Duration
		}
		status struct {
			callback func(field string, val uint32, userdata Userdata)
			closure  Userdata
			vals     []statusvalue
		}
		bind   *BindReq
		unbind *UnbindReq
		auth   struct {
			err error
		}
		return_value int
	}
}
type HookReq struct {
	Type  HookType
	Entry *fq_cmd_instr
}
type BackMessage struct {
	Msg  *Message
	Hreq *HookReq
}
type Client struct {
	host                          string
	port                          uint16
	last_resolve                  time.Time
	Error                         *string
	user, pass, queue, queue_type string
	key                           fq_rk
	cmd_conn, data_conn           net.Conn
	stop                          bool
	cmd_hb_needed                 bool
	cmd_hb_interval               time.Duration
	cmd_hb_max_age                time.Duration
	cmd_hb_last                   time.Time
	peermode                      bool
	qmaxlen                       int
	non_blocking                  bool
	connected                     bool
	data_ready                    bool
	sync_hooks                    bool
	cmdq                          chan *fq_cmd_instr
	q                             chan *Message
	backq                         chan *BackMessage
	hooks                         Hooks
	userdata                      Userdata
	signal                        chan bool
	enqueue_mu                    sync.Mutex
}

func (c *Client) error(err error) {
	var errorstr string = err.Error()
	c.Error = &errorstr
	if c.hooks != nil {
		c.hooks.ErrorLogHook(c, errorstr)
	}
}
func (c *Client) SetUserdata(d Userdata) {
	c.userdata = d
}

func (c *Client) GetUserdata() Userdata {
	return c.userdata
}

func internalClient(peermode bool) Client {
	conn := Client{}
	conn.qmaxlen = 10000
	conn.peermode = peermode
	conn.SetHeartBeat(time.Second)
	return conn
}
func NewClient() Client {
	conn := internalClient(false)
	return conn
}
func NewPeer() Client {
	conn := internalClient(true)
	return conn
}
func (c *Client) SetHooks(hooks Hooks) {
	c.hooks = hooks
}
func (c *Client) Creds(host string, port uint16, sender, pass string) error {
	if c.user != "" {
		return fmt.Errorf("Creds already called")
	}
	sparts := strings.SplitN(sender, "/", 3)
	c.user = sparts[0]
	if len(sparts) > 1 {
		c.queue = sparts[1]
		if len(sparts) > 2 {
			c.queue_type = sparts[2]
		}
	} else {
		id, err := uuid.NewV4()
		if err != nil {
			return err
		}
		c.queue = "q-" + id.String()
	}
	if c.queue_type == "" {
		c.queue_type = FQ_DEFAULT_QUEUE_TYPE
	}
	c.pass = pass

	c.cmdq = make(chan *fq_cmd_instr, 1000)
	c.q = make(chan *Message, c.qmaxlen)
	c.backq = make(chan *BackMessage, c.qmaxlen)
	c.signal = make(chan bool, 1)

	c.host = host
	c.port = port
	return nil
}

func (c *Client) SetHeartBeat(interval time.Duration) {
	if interval > time.Second {
		interval = time.Second
	}
	c.cmd_hb_interval = interval
	c.cmd_hb_max_age = 3 * interval
	if c.data_ready {
		c.HeartBeat()
	}
}
func (c *Client) SetHeartBeatMaxAge(interval time.Duration) {
	c.cmd_hb_max_age = interval
}
func (c *Client) HeartBeat() {
	if c.cmdq == nil {
		return
	}
	e := &fq_cmd_instr{cmd: FQ_PROTO_HBREQ}
	e.data.heartbeat.interval = c.cmd_hb_interval
	c.cmdq <- e
}

func (c *Client) Bind(req *BindReq) {
	if c.cmdq == nil {
		return
	}
	e := &fq_cmd_instr{cmd: FQ_PROTO_BINDREQ}
	e.data.bind = req
	c.cmdq <- e
}

func (c *Client) Unbind(req *UnbindReq) {
	if c.cmdq == nil {
		return
	}
	e := &fq_cmd_instr{cmd: FQ_PROTO_UNBINDREQ}
	e.data.unbind = req
	c.cmdq <- e
}

func (c *Client) Status(f func(string, uint32, Userdata), ud Userdata) {
	if c.cmdq == nil {
		return
	}
	e := &fq_cmd_instr{cmd: FQ_PROTO_STATUSREQ}
	e.data.status.callback = f
	e.data.status.closure = ud
	c.cmdq <- e
}

func (c *Client) SetBacklog(len int) int {
	// We can only set the backlog before we've initialized
	if c.q == nil {
		c.qmaxlen = len
	}
	return c.qmaxlen
}

func (c *Client) SetNonBlocking(nonblock bool) {
	c.non_blocking = nonblock
}

func (c *Client) Connect() error {
	if c.connected {
		return fmt.Errorf("Already connected")
	}
	c.connected = true

	go c.worker()
	go c.data_worker()
	return nil
}

func (c *Client) Destroy() {
	c.stop = true
}

func (c *Client) DataBacklog() int {
	return len(c.q)
}

func (c *Client) Publish(msg *Message) bool {
	if c.non_blocking {
		c.enqueue_mu.Lock()
		defer c.enqueue_mu.Unlock()
		if len(c.q) >= c.qmaxlen {
			return false
		}
		c.q <- msg
	} else {
		c.q <- msg
	}
	return true
}

func (c *Client) handle_hook(e *fq_cmd_instr) {
	if c.hooks == nil {
		return
	}
	switch e.cmd {
	case FQ_PROTO_BINDREQ:
		c.hooks.BindHook(c, e.data.bind)
	case FQ_PROTO_UNBINDREQ:
		c.hooks.UnbindHook(c, e.data.unbind)
	case FQ_PROTO_STATUS:
		if e.data.status.vals != nil {
			f := e.data.status.callback
			closure := e.data.status.closure
			for _, val := range e.data.status.vals {
				f(val.Key, val.Value, closure)
			}
		}
	}
}

func (c *Client) processBackMessage(bm *BackMessage) *Message {
	if bm.Hreq != nil {
		e := bm.Hreq.Entry
		switch bm.Hreq.Type {
		case AUTH_HOOK_TYPE:
			if c.sync_hooks && c.hooks != nil {
				c.hooks.AuthHook(c, e.data.auth.err)
			}
		case CMD_HOOK_TYPE:
			c.handle_hook(e)
		default:
			if c.hooks != nil {
				c.hooks.ErrorLogHook(c, fmt.Sprintf("sync cmd feedback unknown: %v", e.cmd))
			}
		}
	}
	return bm.Msg
}
func (c *Client) Receive(block bool) *Message {
	if block {
		for {
			select {
			case bm := <-c.backq:
				if msg := c.processBackMessage(bm); msg != nil {
					return msg
				}
			}
		}
	}

	select {
	case bm := <-c.backq:
		if msg := c.processBackMessage(bm); msg != nil {
			return msg
		}
	default:
	}
	return nil
}

func (c *Client) data_connect_internal() (net.Conn, error) {
	cmd := uint32(FQ_PROTO_DATA_MODE)
	if c.peermode {
		cmd = uint32(FQ_PROTO_PEER_MODE)
	}
	if c.cmd_conn == nil {
		return nil, fmt.Errorf("no cmd connection")
	}
	connstr := fmt.Sprintf("%s:%d", c.host, c.port)
	timeout := time.Duration(2) * time.Second
	conn, err := net.DialTimeout("tcp", connstr, timeout)
	if err != nil {
		return conn, err
	}
	err = fq_write_uint32(conn, cmd)
	if err != nil {
		return conn, err
	}
	if err := fq_write_short_cmd(conn, uint16(c.key.len), c.key.name[:]); err != nil {
		return conn, err
	}
	return conn, nil
}
func (c *Client) do_auth() error {
	if err := fq_write_uint16(c.cmd_conn, uint16(FQ_PROTO_AUTH_CMD)); err != nil {
		return fmt.Errorf("auth:cmd:" + err.Error())
	}
	if err := fq_write_uint16(c.cmd_conn, uint16(FQ_PROTO_AUTH_PLAIN)); err != nil {
		return fmt.Errorf("auth:plain:" + err.Error())
	}
	user_bytes := []byte(c.user)
	if err := fq_write_short_cmd(c.cmd_conn, uint16(len(user_bytes)), user_bytes); err != nil {
		return fmt.Errorf("auth:user:" + err.Error())
	}
	queue_composed := make([]byte, 0, 256)
	queue_composed = append(queue_composed, []byte(c.queue)...)
	queue_composed = append(queue_composed, byte(0))
	queue_composed = append(queue_composed, []byte(c.queue_type)...)
	if err := fq_write_short_cmd(c.cmd_conn, uint16(len(queue_composed)), queue_composed); err != nil {
		return fmt.Errorf("auth:queue:" + err.Error())
	}
	pass_bytes := []byte(c.pass)
	if err := fq_write_short_cmd(c.cmd_conn, uint16(len(pass_bytes)), pass_bytes); err != nil {
		return fmt.Errorf("auth:pass:" + err.Error())
	}
	if cmd, err := fq_read_uint16(c.cmd_conn); err != nil {
		return fmt.Errorf("auth:response:" + err.Error())
	} else {
		switch cmd {
		case uint16(FQ_PROTO_ERROR):
			return fmt.Errorf("auth:proto_error")
		case uint16(FQ_PROTO_AUTH_RESP):
			if klen, err := fq_read_uint16(c.cmd_conn); err != nil || klen > uint16(cap(c.key.name)) {
				return fmt.Errorf("auth:key:" + err.Error())
			} else {
				err = fq_read_complete(c.cmd_conn, c.key.name[:], int(klen))
				if err != nil {
					return fmt.Errorf("auth:key:" + err.Error())
				}
				c.key.len = uint8(klen)
			}
			c.data_ready = true
		default:
			if c.hooks != nil {
				c.hooks.ErrorLogHook(c, fmt.Sprintf("server auth response 0x%04x unknown", cmd))
			}
			return fmt.Errorf("auth:proto")
		}
	}
	return nil
}
func (c *Client) connect_internal() (net.Conn, error) {
	connstr := fmt.Sprintf("%s:%d", c.host, c.port)
	timeout := time.Duration(2) * time.Second
	conn, err := net.DialTimeout("tcp", connstr, timeout)
	if err != nil {
		return conn, err
	}
	c.cmd_conn = conn
	if err = fq_write_uint32(conn, uint32(FQ_PROTO_CMD_MODE)); err != nil {
		return conn, err
	}
	err = c.do_auth()
	if c.hooks != nil {
		if c.sync_hooks {
			bm := &BackMessage{Hreq: &HookReq{}}
			bm.Hreq.Type = AUTH_HOOK_TYPE
			bm.Hreq.Entry.data.auth.err = err
			c.backq <- bm
		} else {
			c.hooks.AuthHook(c, err)
		}
	}
	c.HeartBeat()
	return conn, err
}

func (c *Client) command_receiver(cmds chan *fq_cmd_instr, cx_queue chan *fq_cmd_instr) {
	var req *fq_cmd_instr = nil
	defer close(cmds)
	for {
		cmd, err := fq_read_uint16(c.cmd_conn)
		if err != nil {
			c.error(err)
			return
		}
		if req == nil {
			select {
			case possible_req, ok := <-cx_queue:
				if !ok {
					return
				}
				req = possible_req
			default:
			}
		}
		switch cmd {
		case uint16(FQ_PROTO_HB):
			c.cmd_hb_last = time.Now()
			c.cmd_hb_needed = true
		case uint16(FQ_PROTO_STATUS):
			if req == nil || req.cmd != FQ_PROTO_STATUSREQ {
				c.error(fmt.Errorf("protocol violation (exp stats)"))
				return
			}
			vals := make([]statusvalue, 0, 30)
			for {
				klen, err := fq_read_uint16(c.cmd_conn)
				if err != nil {
					c.error(err)
					return
				}
				if klen == 0 {
					break
				}
				key := make([]byte, 0, int(klen))
				err = fq_read_complete(c.cmd_conn, key, int(klen))
				if err != nil {
					c.error(err)
					return
				}
				val, err2 := fq_read_uint32(c.cmd_conn)
				if err2 != nil {
					c.error(err2)
					return
				}
				vals = append(vals, statusvalue{Key: string(key), Value: val})
			}
			req.data.status.vals = vals
			cmds <- req
			req = nil
		case uint16(FQ_PROTO_BIND):
			if req == nil || req.cmd != FQ_PROTO_BINDREQ {
				c.error(fmt.Errorf("protocol violation (exp bind, %v)", req))
				return
			}
			routeid, err := fq_read_uint32(c.cmd_conn)
			if err != nil {
				c.error(err)
				return
			}
			req.data.bind.OutRouteId = routeid
			cmds <- req
			req = nil
		case uint16(FQ_PROTO_UNBIND):
			if req == nil || req.cmd != FQ_PROTO_UNBINDREQ {
				c.error(fmt.Errorf("protocol violation (exp unbind)"))
				return
			}
			success, err := fq_read_uint32(c.cmd_conn)
			if err != nil {
				c.error(err)
				return
			}
			req.data.unbind.OutSuccess = success
			cmds <- req
			req = nil
		default:
			c.error(fmt.Errorf("protocol violation: %x", cmd))
			return
		}
	}
}
func (c *Client) command_send(req *fq_cmd_instr, cx_queue chan *fq_cmd_instr) error {
	switch req.cmd {
	case FQ_PROTO_STATUSREQ:
		cx_queue <- req
		return fq_write_uint16(c.cmd_conn, uint16(req.cmd))
	case FQ_PROTO_HBREQ:
		hb_ms := req.data.heartbeat.interval.Nanoseconds() /
			time.Millisecond.Nanoseconds()
		if err := fq_write_uint16(c.cmd_conn, uint16(req.cmd)); err != nil {
			return err
		}
		if err := fq_write_uint16(c.cmd_conn, uint16(hb_ms)); err != nil {
			return err
		}
		c.cmd_hb_interval = req.data.heartbeat.interval
		c.cmd_hb_last = time.Now()
	case FQ_PROTO_BINDREQ:
		cx_queue <- req
		if err := fq_write_uint16(c.cmd_conn, uint16(req.cmd)); err != nil {
			return err
		}
		if err := fq_write_uint16(c.cmd_conn, req.data.bind.Flags); err != nil {
			return err
		}
		if err := fq_write_short_cmd(c.cmd_conn,
			uint16(req.data.bind.Exchange.len),
			req.data.bind.Exchange.name[:]); err != nil {
			return err
		}
		pbytes := []byte(req.data.bind.Program)
		pbytes_len := uint16(len(pbytes))
		if len(pbytes) != int(pbytes_len) {
			return fmt.Errorf("program too long")
		}
		if err := fq_write_short_cmd(c.cmd_conn, pbytes_len, pbytes); err != nil {
			return err
		}
	case FQ_PROTO_UNBINDREQ:
		cx_queue <- req
		if err := fq_write_uint16(c.cmd_conn, uint16(req.cmd)); err != nil {
			return err
		}
		if err := fq_write_uint32(c.cmd_conn, req.data.unbind.RouteId); err != nil {
			return err
		}
		if err := fq_write_short_cmd(c.cmd_conn,
			uint16(req.data.unbind.Exchange.len),
			req.data.unbind.Exchange.name[:]); err != nil {
			return err
		}
	default:
		return fmt.Errorf("can't send unknown cmd: %x", req.cmd)
	}
	return nil
}
func (c *Client) worker_loop() {
	conn, err := c.connect_internal()
	if err != nil {
		if conn != nil {
			conn.Close()
		}
		c.error(err)
		c.signal <- true
		return
	}
	// Let the data channel know it can move forward

	// A go routine is started to read from the wire and put
	// commands into the cmds channel
	cmds := make(chan *fq_cmd_instr, 10)
	// Commands that are send are read from the client cmdq channel
	// and placed into the cx_queue channel, command processing
	// reads from the cmds channel and matches against the cx_queue
	// channel.
	cx_queue := make(chan *fq_cmd_instr, 10)
	hb_chan := make(chan bool, 1)
	hb_quit_chan := make(chan bool, 1)
	go (func(hb chan bool, q chan bool) {
		for keep_going := true; keep_going; {
			select {
			case <-q:
				keep_going = false
			default:
			}
			time.Sleep(c.cmd_hb_interval)
			hb <- true
		}
		close(hb)
	})(hb_chan, hb_quit_chan)

	// command_receiver writes to cmds, so it will close the channel
	// we write to cx_queue via command_send, so we must clost this one
	defer (func() {
		close(cx_queue)
		close(hb_quit_chan)
		conn.Close()
		c.data_ready = false
	})()
	c.signal <- true
	go c.command_receiver(cmds, cx_queue)
	for c.stop == false {
		select {
		case cmd, ok := <-cmds:
			if !ok {
				c.error(fmt.Errorf("reading on command channel terminated"))
				return
			}
			if !c.sync_hooks {
				c.handle_hook(cmd)
			} else {
				bm := &BackMessage{Hreq: &HookReq{}}
				bm.Hreq.Type = CMD_HOOK_TYPE
				bm.Hreq.Entry = cmd
				c.backq <- bm
			}
		case req, ok := <-c.cmdq:
			if !ok {
				c.error(fmt.Errorf("client command queue closed"))
				return
			}
			if err := c.command_send(req, cx_queue); err != nil {
				c.error(err)
				return
			}
		case <-hb_chan:
			if c.cmd_hb_needed {
				if err := fq_write_uint16(c.cmd_conn, uint16(FQ_PROTO_HB)); err != nil {
					c.error(err)
					return
				}
				needed_by := time.Now().Add(-c.cmd_hb_max_age)
				if c.cmd_hb_last.Before(needed_by) {
					c.error(fmt.Errorf("dead: missing heartbeat"))
					return
				}
			}
		}
	}
}
func (c *Client) worker() {
	for c.stop == false {
		c.worker_loop()
		if c.hooks != nil {
			c.hooks.DisconnectHook(c)
		}
	}
}
func (c *Client) data_sender() {
	for c.data_ready {
		msg, ok := <-c.q
		if !ok {
			return
		}
		err := fq_write_msg(c.data_conn, msg, c.peermode)
		if err != nil {
			return
		}
	}
}
func (c *Client) data_receiver() {
	for c.data_ready {
		if msg, err := fq_read_msg(c.data_conn); err != nil {
			c.error(err)
			return
		} else {
			if msg != nil {
				if c.hooks == nil || c.hooks.MessageHook(c, msg) == false {
					c.backq <- &BackMessage{Msg: msg}
				}
			}
		}
	}
}
func (c *Client) data_worker_loop() bool {
	c.data_conn = nil
	conn, err := c.data_connect_internal()
	if err != nil {
		c.error(err)
		return false
	}
	c.data_conn = conn
	defer conn.Close()

	go c.data_sender()
	c.data_receiver()

	return true
}
func (c *Client) data_worker() {
	backoff := 0
	for c.stop == false {
		<-c.signal
		if c.data_ready {
			if c.data_worker_loop() {
				backoff = 0
			}
		}
		if backoff > 0 {
			time.Sleep(time.Duration(backoff+(4096000-(int(rng.Int31())%8192000))) * time.Microsecond)
		} else {
			backoff = 16384000
		}
		if backoff < 1000000000 {
			backoff += (backoff >> 4)
		}
	}
}

// A sample (and useful) Hook binding that allows for simple subscription.

type TransientSubHooks struct {
	MsgsC    chan *Message
	ErrorsC  chan error
	bindings []BindReq
}

func NewTSHooks() TransientSubHooks {
	return TransientSubHooks{
		MsgsC:   make(chan *Message, 10000),
		ErrorsC: make(chan error, 1000),
	}
}
func (h *TransientSubHooks) AuthHook(c *Client, err error) {
	if err != nil {
		h.ErrorsC <- err
		return
	}
	for _, breq := range h.bindings {
		c.Bind(&breq)
	}
}
func (h *TransientSubHooks) AddBinding(exchange, program string) {
	breq := BindReq{
		Exchange: Rk(exchange),
		Flags:    FQ_BIND_TRANS,
		Program:  program,
	}
	h.bindings = append(h.bindings, breq)
}
func (h *TransientSubHooks) BindHook(c *Client, breq *BindReq) {
	if breq.OutRouteId == 0xffffffff {
		h.ErrorsC <- fmt.Errorf("binding failure: %s, %s", breq.Exchange, breq.Program)
	}
}
func (h *TransientSubHooks) UnbindHook(c *Client, breq *UnbindReq) {
}
func (h *TransientSubHooks) CleanupHook(c *Client) {
}
func (h *TransientSubHooks) DisconnectHook(c *Client) {
}
func (h *TransientSubHooks) ErrorLogHook(c *Client, err string) {
	h.ErrorsC <- fmt.Errorf("%s", err)
}
func (h *TransientSubHooks) MessageHook(c *Client, msg *Message) bool {
	h.MsgsC <- msg
	return true
}