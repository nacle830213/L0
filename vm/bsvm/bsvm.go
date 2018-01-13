// Copyright (C) 2017, Beijing Bochen Technology Co.,Ltd.  All rights reserved.
//
// This file is part of L0
//
// The L0 is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// The L0 is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
//
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <http://www.gnu.org/licenses/>.

package bsvm

import (
	"errors"
	"fmt"
	"github.com/bocheninc/L0/components/log"
	"github.com/bocheninc/L0/vm"
	"github.com/bocheninc/L0/vm/luavm"
	"github.com/bocheninc/L0/vm/jsvm"
	"strings"
	"github.com/bocheninc/L0/core/types"
	"github.com/bocheninc/L0/core/params"
	"encoding/json"
	"github.com/bocheninc/L0/core/ledger/state"
)

type WorkerInfo struct {
	allTxsCnt int
	redoTxsCnt int
}

type BsWorker struct {
	isCanRedo bool
	workerID  int
	workerInfo *WorkerInfo
	jsWorker *jsvm.JsWorker
	luaWorker *luavm.LuaWorker
	//can't use lock
}


func NewBsWorker(conf *vm.Config, idx int) *BsWorker {
	bsWorker := &BsWorker{
		workerInfo: &WorkerInfo{},
		jsWorker: jsvm.NewJsWorker(conf),
		luaWorker:luavm.NewLuaWorker(conf),
		workerID: idx,
	}

	return bsWorker
}

func (worker *BsWorker) FetchContractType(workerProcWithCallback *vm.WorkerProcWithCallback) string {
	var err error
	txType := "unknown"
	//callback := func() {
	//	err := workerProcWithCallback.WorkProc.L0Handler.CallBack(&state.CallBackResponse{
	//		IsCanRedo: !worker.isCanRedo,
	//		Err: err,
	//	})
	//	if err != nil  && !worker.isCanRedo {
	//		log.Errorf("ThreadId: %+v, tx redo, tx_hash: %+v, tx_idx: %+v, err: %+v, can Redo: %+v",
	//			worker.workerID, workerProcWithCallback.WorkProc.ContractData.Transaction.Hash().String(), workerProcWithCallback.Idx, err, worker.isCanRedo)
	//		worker.isCanRedo = true
	//		worker.FetchContractType(workerProcWithCallback)
	//	}
	//}

	if workerProcWithCallback.WorkProc.ContractData.Transaction.GetType() == types.TypeContractInvoke {
		txType, err = worker.GetInvokeType(workerProcWithCallback)
		if err != nil {
			log.Errorf("ThreadId: %+v, can't execute contract, tx_hash: %s, tx_idx: %+v, err_msg: %+v, can Redo: %+v", worker.workerID,
				workerProcWithCallback.WorkProc.ContractData.Transaction.Hash().String(), workerProcWithCallback.Idx, err.Error(), worker.isCanRedo)
		}
	} else {
		txType = worker.GetInitType(workerProcWithCallback)
	}

	return txType
}

func (worker *BsWorker) ExecJob(workerProcWithCallback *vm.WorkerProcWithCallback) error {
	var res interface{}
	var err error
	if worker.isCommonTransaction(workerProcWithCallback) {
		err = worker.ExecCommonTransaction(workerProcWithCallback)
	} else {
		txType := worker.FetchContractType(workerProcWithCallback)
		if strings.Contains(txType, "lua"){
			res, err = worker.luaWorker.VmJob(workerProcWithCallback)
		} else if strings.Contains(txType, "js") {
			res, err = worker.jsWorker.VmJob(workerProcWithCallback)
		} else {
			log.Errorf("can't find tx type: %+v, %+v",
				workerProcWithCallback.WorkProc.ContractData.Transaction.Hash().String(),
				workerProcWithCallback.WorkProc.ContractData.Transaction.GetType())
			err = errors.New("find contract type fail ...")
		}
	}


	if workerProcWithCallback.Idx != 0 {
		if !worker.isCanRedo {
			vm.Txsync.Wait(workerProcWithCallback.Idx%vm.VMConf.BsWorkerCnt)
		}
	}
	log.Debugf("====> worker thread id: %+v, tx_idx: %+v, tx_hash: %+v",
		worker.workerID, workerProcWithCallback.Idx, workerProcWithCallback.WorkProc.ContractData.Transaction.Hash().String())

	res = res
	cerr := workerProcWithCallback.WorkProc.L0Handler.CallBack(&state.CallBackResponse{
		IsCanRedo: !worker.isCanRedo,
		Err: err,
		//Result: res.(string),
	})

	return cerr
}

func (worker *BsWorker) VmJob(data interface{}) (interface{}, error) {
	workerProcWithCallback := data.(*vm.WorkerProcWithCallback)
	log.Debugf("worker thread id: %+v, start tx: %+v, tx_idx: %+v", worker.workerID, workerProcWithCallback.WorkProc.ContractData.Transaction.Hash().String(), workerProcWithCallback.Idx)
	defer log.Debugf("worker thread id: %+v, finish tx: %+v, tx_idx: %+v", worker.workerID, workerProcWithCallback.WorkProc.ContractData.Transaction.Hash().String(), workerProcWithCallback.Idx)
	worker.isCanRedo = false
	err := worker.ExecJob(workerProcWithCallback)


	if err != nil && !worker.isCanRedo {
		log.Errorf("====> worker thread id: %+v, to tx redo, tx_hash: %+v, tx_idx: %+v, cause: %+v",
			worker.workerID, workerProcWithCallback.WorkProc.ContractData.Transaction.Hash().String(), workerProcWithCallback.Idx, err)
		worker.isCanRedo = true
		err := worker.ExecJob(workerProcWithCallback)
		if err != nil {
			log.Errorf("====> worker thread id: %+v, tx redo failed, tx_hash: %+v, tx_idx: %+v, cause: %+v",
				worker.workerID, workerProcWithCallback.WorkProc.ContractData.Transaction.Hash().String(), workerProcWithCallback.Idx, err)
		}
	}

	vm.Txsync.Notify((workerProcWithCallback.Idx+1)%vm.VMConf.BsWorkerCnt)

	return nil, nil
}

func (worker *BsWorker) VmReady() bool {
	return true
}

func (worker *BsWorker) VmInitialize() {
	//pass
}

func (worker *BsWorker) VmTerminate() {
	//pass
}

func (worker *BsWorker) GetInvokeType(wpwc *vm.WorkerProcWithCallback) (string, error) {
	var err error
	cc := new(vm.ContractCode)
	var code []byte
	if len(wpwc.WorkProc.ContractData.ContractAddr) == 0 {
		code, err = wpwc.WorkProc.L0Handler.GetGlobalState(params.GlobalContractKey)
	} else {
		code, err = wpwc.WorkProc.L0Handler.GetState(vm.ContractCodeKey)
	}

	if len(code) != 0 && err == nil {
		contractCode, err := vm.DoContractStateData(code)
		if err != nil {
			return "", fmt.Errorf("cat't find contract code in db(1), err: %+v, contract_addr: %+v, len(code): %+v",
				err, wpwc.WorkProc.ContractData.ContractAddr, len(code))
		}
		err = json.Unmarshal(contractCode, cc)
		if err != nil {
			return "", fmt.Errorf("cat't find contract code in db(2), err: %+v, contract_addr: %+v, len(code): %+v", err, wpwc.WorkProc.ContractData.ContractAddr, len(code))
		}
		wpwc.WorkProc.ContractData.ContractCode = string(cc.Code)
		return cc.Type, nil
	} else {
		return "", errors.New(fmt.Sprintf("can't find contract code in db,err: %+v, addr: %+v, len(code): %+v",
			err, wpwc.WorkProc.ContractData.ContractAddr, len(code)))
	}
}

func (worker *BsWorker) GetInitType(wpwc *vm.WorkerProcWithCallback) string {
	txType := wpwc.WorkProc.ContractData.Transaction.GetType()
	if txType == types.TypeLuaContractInit {
		return "lua"
	} else {
		return "js"
	}
}

func (worker *BsWorker) isCommonTransaction(wpwc *vm.WorkerProcWithCallback) bool {
	txType := wpwc.WorkProc.ContractData.Transaction.GetType()
	if txType == types.TypeLuaContractInit || txType == types.TypeContractInvoke ||
		txType == types.TypeJSContractInit || txType == types.TypeContractQuery {
		return false
	}

	return true
}


func (worker *BsWorker) ExecCommonTransaction(wpwc *vm.WorkerProcWithCallback) error {
	return wpwc.WorkProc.L0Handler.Transfer(wpwc.WorkProc.ContractData.Transaction)
}