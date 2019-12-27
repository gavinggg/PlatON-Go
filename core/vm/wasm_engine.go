package vm

import (
	"github.com/PlatONnetwork/PlatON-Go/log"
	"github.com/PlatONnetwork/PlatON-Go/rlp"

	"github.com/PlatONnetwork/PlatON-Go/core/lru"

	"github.com/PlatONnetwork/wagon/exec"
	"github.com/pkg/errors"
)

const (
	callEntryName = "invoke"
	initFn        = "init"
)
const memoryLimit = 16 * 1014 * 1024

const (
	verifyModule   = true
	unVerifyModule = false
)

type wagonEngineCreator struct {
}

func (w *wagonEngineCreator) Create(evm *EVM, config Config, contract *Contract) (wasmEngine, error) {
	return &wagonEngine{
		evm:      evm,
		config:   config,
		contract: contract,
	}, nil
}

type wasmEngine interface {
	Run(input []byte, readOnly bool) (ret []byte, err error)
}

type wagonEngine struct {
	evm      *EVM
	config   Config
	vm       *exec.CompileVM
	contract *Contract
}

func (engine *wagonEngine) EVM() *EVM {
	return engine.evm
}

func (engine *wagonEngine) Config() Config {
	return engine.config
}

func (engine *wagonEngine) StateDB() StateDB {
	return engine.evm.StateDB
}

func (engine *wagonEngine) Contract() *Contract {
	return engine.contract
}

func (engine *wagonEngine) Run(input []byte, readOnly bool) ([]byte, error) {

	var deploy bool

	if len(input) == 0 { // deploy contract
		deploy = true
		contractCode, calldata, err := assemblyDeployCode(engine.Contract().Code)
		if nil != err {
			return nil, err
		}
		engine.Contract().Code = contractCode
		input = calldata
	} else { // call contract
	}

	module, entryIndex, moduleErr := engine.MakeModule(deploy)
	if nil != moduleErr {
		return nil, moduleErr
	}

	if err := engine.prepare(module, input, readOnly); nil != err {
		return nil, err
	}

	//exec vm
	ret, err := engine.exec(entryIndex)
	if deploy {
		return engine.Contract().Code, err
	}
	return ret, err
}

func (engine *wagonEngine) prepare(module *exec.CompiledModule, input []byte, readOnly bool) error {
	vm, err := exec.NewVMWithCompiled(module, memoryLimit)
	if nil != err {
		return err
	}
	vm.RecoverPanic = true
	ctx := &VMContext{
		evm:      engine.EVM(),
		contract: engine.Contract(),
		config:   engine.Config(),
		db:       engine.StateDB(),
		Input:    input, //set input bytes
		log:      NewWasmLogger(engine.config, log.WasmRoot()),
		readOnly: readOnly,
	}
	vm.SetHostCtx(ctx)
	engine.vm = vm
	return nil
}

func (engine *wagonEngine) exec(index int64) ([]byte, error) {
	_, err := engine.vm.ExecCode(index)
	if err != nil {
		return nil, errors.Wrap(err, "execute function code")
	}
	ctx := engine.vm.HostCtx().(*VMContext)

	switch {
	case ctx.Revert:
		return nil, errExecutionReverted
	case engine.vm.Abort():
		return nil, ErrAbort
	case err != nil:
		return nil, errors.Wrap(err, "execute function code")
	}
	return ctx.Output, err
}

func (engine *wagonEngine) MakeModule(deploy bool) (*exec.CompiledModule, int64, error) {
	if !deploy {
		return engine.makeModuleWithCall()
	} else {
		return engine.makeModuleWithDeploy()
	}
}

func (engine *wagonEngine) makeModuleWithDeploy() (*exec.CompiledModule, int64, error) {

	cache := &lru.WasmModule{}
	module, err := ReadWasmModule(engine.Contract().Code, verifyModule)
	if nil != err {
		return nil, 0, err
	}
	// Short circuit if the `invoke` function is not existing in the module
	entry, ok := module.RawModule.Export.Entries[callEntryName]
	if !ok {
		return nil, 0, errors.New("The skeleton of the contract is illegal")
	}
	index := int64(entry.Index)
	fidx := module.RawModule.Function.Types[int(index)]
	ftype := module.RawModule.Types.Entries[int(fidx)]

	if len(ftype.ParamTypes) > 0 || len(ftype.ReturnTypes) > 0 {
		return nil, 0, errors.New("function sig error")
	}

	cache.Module = module
	lru.WasmCache().Add(*(engine.Contract().CodeAddr), cache)
	return module, 0, nil
}

func (engine *wagonEngine) makeModuleWithCall() (*exec.CompiledModule, int64, error) {

	// load module
	cache, ok := lru.WasmCache().Get(engine.Contract().Address())
	if !ok || (ok && nil == cache.Module) {
		cache = &lru.WasmModule{}

		module, err := ReadWasmModule(engine.Contract().Code, unVerifyModule)
		if nil != err {
			return nil, 0, err
		}

		cache.Module = module
		lru.WasmCache().Add(engine.Contract().Address(), cache)
	}

	mod := cache.Module
	entry, ok := mod.RawModule.Export.Entries[callEntryName]
	if !ok {
		return nil, 0, errors.New("The contract hadn't invoke fn")
	}
	index := int64(entry.Index)
	return mod, index, nil
}

func assemblyDeployCode(code []byte) (contractCode []byte, calldata []byte, err error) {
	if len(code) == 0 {
		return nil, nil, errors.New("No contract code to be parsed")
	}

	var data [][]byte
	if err = rlp.DecodeBytes(code, &data); nil != err {
		return
	}

	if len(data) == 0 {
		return nil, nil, errors.New("No contract code to be parsed")
	}

	// decoding the contract code
	if err = rlp.DecodeBytes(data[0], &contractCode); nil != err {
		return
	}

	initByte, err := rlp.EncodeToBytes(initFn)
	if nil != err {
		return
	}
	data[0] = initByte

	calldata, err = rlp.EncodeToBytes(data)
	if nil != err {
		return
	}
	return
}
