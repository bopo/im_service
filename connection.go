/**
 * Copyright (c) 2014-2015, GoBelieve     
 * All rights reserved.
 *
 * This program is free software; you can redistribute it and/or modify
 * it under the terms of the GNU General Public License as published by
 * the Free Software Foundation; either version 2 of the License, or
 * (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU General Public License for more details.
 *
 * You should have received a copy of the GNU General Public License
 * along with this program; if not, write to the Free Software
 * Foundation, Inc., 59 Temple Place, Suite 330, Boston, MA  02111-1307  USA
 */

package main

import "net"
import "time"
import "sync"
import "sync/atomic"
import log "github.com/golang/glog"
import "github.com/googollee/go-engine.io"

const CLIENT_TIMEOUT = (60 * 6)

type Connection struct {
	conn   interface{}
	closed int32
	forbidden int32 //是否被禁言

	tc     int32 //write channel timeout count
	wt     chan *Message
	ewt    chan *EMessage //在线消息
	owt    chan *EMessage //离线消息

	//客户端协议版本号
	version int

	tm     time.Time
	appid  int64
	uid    int64
	device_id string
	device_ID int64 //generated by device_id + platform_id
	platform_id int8

	unackMessages map[int]*EMessage
	unacks map[int]int64
	mutex  sync.Mutex
}

func (client *Connection) SendMessage(uid int64, msg *Message) bool {
	return Send0Message(client.appid, uid, msg)
}

func (client *Connection) EnqueueMessage(msg *Message) bool {
	closed := atomic.LoadInt32(&client.closed)
	if closed > 0 {
		log.Infof("can't send message to closed connection:%d", client.uid)
		return false
	}

	tc := atomic.LoadInt32(&client.tc)
	if tc > 0 {
		log.Infof("can't send message to blocked connection:%d", client.uid)
		atomic.AddInt32(&client.tc, 1)
		return false
	}
	select {
	case client.wt <- msg:
		return true
	case <- time.After(60*time.Second):
		atomic.AddInt32(&client.tc, 1)
		log.Infof("send message to wt timed out:%d", client.uid)
		return false
	}
}

func (client *Connection) EnqueueEMessage(emsg *EMessage) {
	closed := atomic.LoadInt32(&client.closed)
	if closed > 0 {
		log.Infof("can't send message to closed connection:%d", client.uid)
		return
	}

	tc := atomic.LoadInt32(&client.tc)
	if tc > 0 {
		log.Infof("can't send message to blocked connection:%d", client.uid)
		atomic.AddInt32(&client.tc, 1)
		return
	}
	select {
	case client.ewt <- emsg:
		break
	case <- time.After(60*time.Second):
		atomic.AddInt32(&client.tc, 1)
		log.Infof("send message to ewt timed out:%d", client.uid)
	}
}

func (client *Connection) EnqueueOfflineMessage(emsg *EMessage) {
	tc := atomic.LoadInt32(&client.tc)
	if tc > 0 {
		log.Infof("can't send message to blocked connection:%d", client.uid)
		atomic.AddInt32(&client.tc, 1)
		return
	}
	select {
	case client.owt <- emsg:
		break
	case <- time.After(60*time.Second):
		atomic.AddInt32(&client.tc, 1)
		log.Infof("send message to owt timed out:%d", client.uid)
	}
}


// 根据连接类型获取消息
func (client *Connection) read() *Message {
	if conn, ok := client.conn.(net.Conn); ok {
		conn.SetReadDeadline(time.Now().Add(CLIENT_TIMEOUT * time.Second))
		return ReceiveMessage(conn)
	} else if conn, ok := client.conn.(engineio.Conn); ok {
		return ReadEngineIOMessage(conn)
	}
	return nil
}

// 根据连接类型发送消息
func (client *Connection) send(msg *Message) {
	if conn, ok := client.conn.(net.Conn); ok {
		tc := atomic.LoadInt32(&client.tc)
		if tc > 0 {
			log.Info("can't write data to blocked socket")
			return
		}
		conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
		err := SendMessage(conn, msg)
		if err != nil {
			atomic.AddInt32(&client.tc, 1)
			log.Info("send msg:", Command(msg.cmd),  " tcp err:", err)
		}
	} else if conn, ok := client.conn.(engineio.Conn); ok {
		SendEngineIOBinaryMessage(conn, msg)
	}
}

// 根据连接类型关闭
func (client *Connection) close() {
	if conn, ok := client.conn.(net.Conn); ok {
		conn.Close()
	} else if conn, ok := client.conn.(engineio.Conn); ok {
		conn.Close()
	}
}

func (client *Connection) RemoveUnAckMessage(ack *MessageACK) *EMessage {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	var msgid int64
	var msg *Message
	var ok bool

	seq := int(ack.seq)
	if msgid, ok = client.unacks[seq]; ok {
		log.Infof("dequeue offline msgid:%d uid:%d\n", msgid, client.uid)
		delete(client.unacks, seq)
	} else {
		log.Warning("can't find msgid with seq:", seq)
	}
	if emsg, ok := client.unackMessages[seq]; ok {
		msg = emsg.msg
		delete(client.unackMessages, seq)
	}

	return &EMessage{msgid:msgid, msg:msg}
}

func (client *Connection) AddUnAckMessage(emsg *EMessage) {
	client.mutex.Lock()
	defer client.mutex.Unlock()
	seq := emsg.msg.seq
	client.unacks[seq] = emsg.msgid
	if emsg.msg.cmd == MSG_IM || emsg.msg.cmd == MSG_GROUP_IM {
		client.unackMessages[seq] = emsg
	}
}
