package dapp

//package none execer for unknow execer
//all none transaction exec ok, execept nofee
//nofee transaction will not pack into block

import (
	"bytes"
	"fmt"
	"reflect"

	log "github.com/inconshreveable/log15"
	"gitlab.33.cn/chain33/chain33/account"
	"gitlab.33.cn/chain33/chain33/client"
	"gitlab.33.cn/chain33/chain33/common/address"
	dbm "gitlab.33.cn/chain33/chain33/common/db"
	"gitlab.33.cn/chain33/chain33/types"
)

var blog = log.New("module", "execs.base")

type Driver interface {
	SetStateDB(dbm.KV)
	GetCoinsAccount() *account.DB
	SetLocalDB(dbm.KVDB)
	//当前交易执行器名称
	GetCurrentExecName() string
	//驱动的名字，这个名称是固定的
	GetDriverName() string
	//执行器的别名(一个驱动(code),允许创建多个执行器，类似evm一份代码可以创建多个合约）
	GetName() string
	//设置执行器的真实名称
	SetName(string)
	SetCurrentExecName(string)
	Allow(tx *types.Transaction, index int) error
	IsFriend(myexec []byte, writekey []byte, othertx *types.Transaction) bool
	GetActionName(tx *types.Transaction) string
	SetEnv(height, blocktime int64, difficulty uint64)
	CheckTx(tx *types.Transaction, index int) error
	Exec(tx *types.Transaction, index int) (*types.Receipt, error)
	ExecLocal(tx *types.Transaction, receipt *types.ReceiptData, index int) (*types.LocalDBSet, error)
	ExecDelLocal(tx *types.Transaction, receipt *types.ReceiptData, index int) (*types.LocalDBSet, error)
	Query(funcName string, params []byte) (types.Message, error)
	IsFree() bool
	SetApi(client.QueueProtocolAPI)
	SetTxs(txs []*types.Transaction)
	SetReceipt(receipts []*types.ReceiptData)

	//GetTxs and TxGroup
	GetTxs() []*types.Transaction
	GetTxGroup(index int) ([]*types.Transaction, error)
	GetPayloadValue() types.Message
	GetTypeMap() map[string]int32
	GetFuncMap() map[string]reflect.Method
}

type DriverBase struct {
	statedb      dbm.KV
	localdb      dbm.KVDB
	coinsaccount *account.DB
	height       int64
	blocktime    int64
	name         string
	curname      string
	child        Driver
	childValue   reflect.Value
	isFree       bool
	difficulty   uint64
	api          client.QueueProtocolAPI
	txs          []*types.Transaction
	receipts     []*types.ReceiptData
	ety          types.ExecutorType
}

func (d *DriverBase) GetPayloadValue() types.Message {
	if d.ety == nil {
		return nil
	}
	return d.ety.GetPayload()
}

func (d *DriverBase) GetTypeMap() map[string]int32 {
	return nil
}

func (d *DriverBase) GetFuncMap() map[string]reflect.Method {
	return nil
}

func (d *DriverBase) SetApi(api client.QueueProtocolAPI) {
	d.api = api
}

func (d *DriverBase) GetApi() client.QueueProtocolAPI {
	return d.api
}

func (d *DriverBase) SetEnv(height, blocktime int64, difficulty uint64) {
	d.height = height
	d.blocktime = blocktime
	d.difficulty = difficulty
}

func (d *DriverBase) SetIsFree(isFree bool) {
	d.isFree = isFree
}

func (d *DriverBase) IsFree() bool {
	return d.isFree
}

func (d *DriverBase) SetExecutorType(e types.ExecutorType) {
	d.ety = e
}

func (d *DriverBase) SetChild(e Driver) {
	d.child = e
	d.childValue = reflect.ValueOf(e)
}

func (d *DriverBase) ExecLocal(tx *types.Transaction, receipt *types.ReceiptData, index int) (*types.LocalDBSet, error) {
	var set types.LocalDBSet
	//保存：tx
	kv := d.GetTx(tx, receipt, index)
	set.KV = append(set.KV, kv...)
	//保存: from/to
	txindex := d.getTxIndex(tx, receipt, index)
	txinfobyte := types.Encode(txindex.index)
	if len(txindex.from) != 0 {
		fromkey1 := CalcTxAddrDirHashKey(txindex.from, TxIndexFrom, txindex.heightstr)
		fromkey2 := CalcTxAddrHashKey(txindex.from, txindex.heightstr)
		set.KV = append(set.KV, &types.KeyValue{fromkey1, txinfobyte})
		set.KV = append(set.KV, &types.KeyValue{fromkey2, txinfobyte})
		kv, err := updateAddrTxsCount(d.GetLocalDB(), txindex.from, 1, true)
		if err == nil && kv != nil {
			set.KV = append(set.KV, kv)
		}
	}
	if len(txindex.to) != 0 {
		tokey1 := CalcTxAddrDirHashKey(txindex.to, TxIndexTo, txindex.heightstr)
		tokey2 := CalcTxAddrHashKey(txindex.to, txindex.heightstr)
		set.KV = append(set.KV, &types.KeyValue{tokey1, txinfobyte})
		set.KV = append(set.KV, &types.KeyValue{tokey2, txinfobyte})
		kv, err := updateAddrTxsCount(d.GetLocalDB(), txindex.to, 1, true)
		if err == nil && kv != nil {
			set.KV = append(set.KV, kv)
		}
	}
	lset, err := d.callLocal("ExecLocal_", tx, receipt, index)
	if err != nil {
		blog.Debug("call ExecLocal", "tx.Execer", string(tx.Execer), "err", err)
		return &set, nil
	}
	//merge
	if lset != nil && lset.KV != nil {
		set.KV = append(set.KV, lset.KV...)
	}
	return &set, nil
}

//获取公共的信息
func (d *DriverBase) GetTx(tx *types.Transaction, receipt *types.ReceiptData, index int) []*types.KeyValue {
	txhash := tx.Hash()
	//构造txresult 信息保存到db中
	var txresult types.TxResult
	txresult.Height = d.GetHeight()
	txresult.Index = int32(index)
	txresult.Tx = tx
	txresult.Receiptdate = receipt
	txresult.Blocktime = d.GetBlockTime()
	txresult.ActionName = d.child.GetActionName(tx)
	var kvlist []*types.KeyValue
	kvlist = append(kvlist, &types.KeyValue{Key: types.CalcTxKey(txhash), Value: types.Encode(&txresult)})
	if types.IsEnable("quickIndex") {
		kvlist = append(kvlist, &types.KeyValue{Key: types.CalcTxShortKey(txhash), Value: []byte("1")})
	}
	return kvlist
}

type txIndex struct {
	from      string
	to        string
	heightstr string
	index     *types.ReplyTxInfo
}

//交易中 from/to 的索引
func (d *DriverBase) getTxIndex(tx *types.Transaction, receipt *types.ReceiptData, index int) *txIndex {
	var txIndexInfo txIndex
	var txinf types.ReplyTxInfo
	txinf.Hash = tx.Hash()
	txinf.Height = d.GetHeight()
	txinf.Index = int64(index)

	txIndexInfo.index = &txinf
	heightstr := fmt.Sprintf("%018d", d.GetHeight()*types.MaxTxsPerBlock+int64(index))
	txIndexInfo.heightstr = heightstr

	txIndexInfo.from = address.PubKeyToAddress(tx.GetSignature().GetPubkey()).String()
	txIndexInfo.to = tx.GetRealToAddr()
	return &txIndexInfo
}

func (d *DriverBase) ExecDelLocal(tx *types.Transaction, receipt *types.ReceiptData, index int) (*types.LocalDBSet, error) {
	var set types.LocalDBSet
	//del：tx
	kvdel := d.GetTx(tx, receipt, index)
	for k := range kvdel {
		kvdel[k].Value = nil
	}
	//del: addr index
	txindex := d.getTxIndex(tx, receipt, index)
	if len(txindex.from) != 0 {
		fromkey1 := CalcTxAddrDirHashKey(txindex.from, TxIndexFrom, txindex.heightstr)
		fromkey2 := CalcTxAddrHashKey(txindex.from, txindex.heightstr)
		set.KV = append(set.KV, &types.KeyValue{Key: fromkey1, Value: nil})
		set.KV = append(set.KV, &types.KeyValue{Key: fromkey2, Value: nil})
		kv, err := updateAddrTxsCount(d.GetLocalDB(), txindex.from, 1, false)
		if err == nil && kv != nil {
			set.KV = append(set.KV, kv)
		}
	}
	if len(txindex.to) != 0 {
		tokey1 := CalcTxAddrDirHashKey(txindex.to, TxIndexTo, txindex.heightstr)
		tokey2 := CalcTxAddrHashKey(txindex.to, txindex.heightstr)
		set.KV = append(set.KV, &types.KeyValue{Key: tokey1, Value: nil})
		set.KV = append(set.KV, &types.KeyValue{Key: tokey2, Value: nil})
		kv, err := updateAddrTxsCount(d.GetLocalDB(), txindex.to, 1, false)
		if err == nil && kv != nil {
			set.KV = append(set.KV, kv)
		}
	}
	set.KV = append(set.KV, kvdel...)

	lset, err := d.callLocal("ExecDelLocal_", tx, receipt, index)
	if err != nil {
		blog.Error("call ExecDelLocal", "execer", string(tx.Execer), "err", err)
		return &set, nil
	}
	//merge
	if lset != nil && lset.KV != nil {
		set.KV = append(set.KV, lset.KV...)
	}
	return &set, nil
}

func (d *DriverBase) callLocal(prefix string, tx *types.Transaction, receipt *types.ReceiptData, index int) (set *types.LocalDBSet, err error) {
	if d.ety == nil {
		return nil, types.ErrActionNotSupport
	}
	name, value, err := d.ety.DecodePayloadValue(tx)
	if err != nil {
		return nil, err
	}
	//call action
	funcname := prefix + name
	funcmap := d.child.GetFuncMap()
	if _, ok := funcmap[funcname]; !ok {
		return nil, types.ErrActionNotSupport
	}
	valueret := funcmap[funcname].Func.Call([]reflect.Value{d.childValue, value, reflect.ValueOf(tx), reflect.ValueOf(receipt), reflect.ValueOf(index)})
	if !types.IsOK(valueret, 2) {
		return nil, types.ErrMethodReturnType
	}
	r1 := valueret[0].Interface()
	if r1 != nil {
		if r, ok := r1.(*types.LocalDBSet); ok {
			set = r
		} else {
			return nil, types.ErrMethodReturnType
		}
	}
	r2 := valueret[1].Interface()
	err = nil
	if r2 != nil {
		if r, ok := r2.(error); ok {
			err = r
		} else {
			return nil, types.ErrMethodReturnType
		}
	}
	return set, err
}

func (d *DriverBase) checkAddress(addr string) error {
	if IsDriverAddress(addr, d.height) {
		return nil
	}
	return address.CheckAddress(addr)
}

//调用子类的CheckTx, 也可以不调用，实现自己的CheckTx
func (d *DriverBase) Exec(tx *types.Transaction, index int) (receipt *types.Receipt, err error) {
	//to 必须是一个地址
	if err := d.checkAddress(tx.GetRealToAddr()); err != nil {
		return nil, err
	}
	if err := d.child.CheckTx(tx, index); err != nil {
		return nil, err
	}
	//为了兼容原来的系统,多加了一个判断
	if d.child.GetPayloadValue() == nil {
		return nil, nil
	}
	name, value, err := d.ety.DecodePayloadValue(tx)
	if err != nil {
		return nil, err
	}
	funcmap := d.child.GetFuncMap()
	funcname := "Exec_" + name
	if _, ok := funcmap[funcname]; !ok {
		return nil, types.ErrActionNotSupport
	}
	valueret := funcmap[funcname].Func.Call([]reflect.Value{d.childValue, value, reflect.ValueOf(tx), reflect.ValueOf(index)})
	if !types.IsOK(valueret, 2) {
		return nil, types.ErrMethodReturnType
	}
	//参数1
	r1 := valueret[0].Interface()
	if r1 != nil {
		if r, ok := r1.(*types.Receipt); ok {
			receipt = r
		} else {
			return nil, types.ErrMethodReturnType
		}
	}
	//参数2
	r2 := valueret[1].Interface()
	err = nil
	if r2 != nil {
		if r, ok := r2.(error); ok {
			err = r
		} else {
			return nil, types.ErrMethodReturnType
		}
	}
	return receipt, err
}

//默认情况下，tx.To 地址指向合约地址
func (d *DriverBase) CheckTx(tx *types.Transaction, index int) error {
	execer := string(tx.Execer)
	if ExecAddress(execer) != tx.To {
		return types.ErrToAddrNotSameToExecAddr
	}
	return nil
}

func (d *DriverBase) SetStateDB(db dbm.KV) {
	if d.coinsaccount == nil {
		//log.Error("new CoinsAccount")
		d.coinsaccount = account.NewCoinsAccount()
	}
	d.statedb = db
	d.coinsaccount.SetDB(db)
}

func (d *DriverBase) GetTxGroup(index int) ([]*types.Transaction, error) {
	if len(d.txs) <= index {
		return nil, types.ErrTxGroupIndex
	}
	tx := d.txs[index]
	c := int(tx.GroupCount)
	if c <= 0 || c > int(types.MaxTxGroupSize) {
		return nil, types.ErrTxGroupCount
	}
	for i := index; i >= 0 && i >= index-c; i-- {
		if bytes.Equal(d.txs[i].Header, d.txs[i].Hash()) { //find header
			txgroup := types.Transactions{Txs: d.txs[i : i+c]}
			err := txgroup.Check(d.GetHeight(), types.MinFee)
			if err != nil {
				return nil, err
			}
			return txgroup.Txs, nil
		}
	}
	return nil, types.ErrTxGroupFormat
}

func (d *DriverBase) GetReceipt() []*types.ReceiptData {
	return d.receipts
}

func (d *DriverBase) SetReceipt(receipts []*types.ReceiptData) {
	d.receipts = receipts
}

func (d *DriverBase) GetStateDB() dbm.KV {
	return d.statedb
}

func (d *DriverBase) SetLocalDB(db dbm.KVDB) {
	d.localdb = db
}

func (d *DriverBase) GetLocalDB() dbm.KVDB {
	return d.localdb
}

func (d *DriverBase) GetHeight() int64 {
	return d.height
}

func (d *DriverBase) GetBlockTime() int64 {
	return d.blocktime
}

func (d *DriverBase) GetDifficulty() uint64 {
	return d.difficulty
}

func (d *DriverBase) GetName() string {
	if d.name == "" {
		return d.child.GetDriverName()
	}
	return d.name
}

func (d *DriverBase) GetCurrentExecName() string {
	if d.curname == "" {
		return d.child.GetDriverName()
	}
	return d.curname
}

func (d *DriverBase) SetName(name string) {
	d.name = name
}

func (d *DriverBase) SetCurrentExecName(name string) {
	d.curname = name
}

func (d *DriverBase) GetActionName(tx *types.Transaction) string {
	return tx.ActionName()
}

func (d *DriverBase) CheckSignatureData(tx *types.Transaction, index int) bool {
	return true
}

func (d *DriverBase) GetCoinsAccount() *account.DB {
	if d.coinsaccount == nil {
		d.coinsaccount = account.NewCoinsAccount()
		d.coinsaccount.SetDB(d.statedb)
	}
	return d.coinsaccount
}

func (d *DriverBase) GetTxs() []*types.Transaction {
	return d.txs
}

func (d *DriverBase) SetTxs(txs []*types.Transaction) {
	d.txs = txs
}