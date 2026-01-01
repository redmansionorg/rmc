# OTS 集成到 RMC(BSC) 原生代码修改指南

> **重要**: 本文档记录了 OTS 模块集成到 RMC(BSC) 时需要修改的所有原生文件。
> 每次同步 BSC 上游代码时，这些修改可能被覆盖，需要按本文档重新应用。

## 修改概览

| 文件 | 修改类型 | 代码量 | 用途 |
|------|---------|--------|------|
| `consensus/parlia/parlia.go` | 修改 | ~25行 | 在出块时注入 OTS 系统交易 |
| `eth/backend.go` | 修改 | ~25行 | 初始化 OTS 模块 |
| `eth/ethconfig/config.go` | 修改 | ~50行 | 添加 OTSConfig 结构体 |
| `eth/ethconfig/gen_config.go` | 修改 | ~10行 | TOML 序列化支持 |
| `internal/flags/categories.go` | 修改 | ~1行 | 添加 OTS 分类 |
| `cmd/utils/flags.go` | 修改 | ~70行 | CLI flags 定义和处理 |
| `cmd/geth/main.go` | 修改 | ~1行 | 注册 OTS flags |

**总计: 7个文件, ~180行代码**

---

## 1. consensus/parlia/parlia.go

### 目的
在 Parlia 共识引擎的 `FinalizeAndAssemble` 方法中调用 OTS Hook，
使矿工在出块时能够注入 OTS 系统交易（用于将 Merkle Root 锚定到链上）。

### 修改位置
文件顶部 import 区域 + `FinalizeAndAssemble` 函数内部

### 具体修改

#### 1.1 添加 Import

在 import 区域添加：

```go
import (
    // ... 现有 imports ...
    "github.com/ethereum/go-ethereum/ots/hook"  // 添加此行
)
```

#### 1.2 在 FinalizeAndAssemble 中添加 OTS Hook 调用

在 `FinalizeAndAssemble` 函数中，找到 `updateValidatorSetV2` 调用之后，
`return nil` 之前，添加以下代码：

```go
// OTS: Invoke finalize hook to inject OTS system transactions
// This must be called after all other system transactions are processed
// The hook follows the Fail-Open pattern: errors are logged but never block consensus
if otsTxs := hook.InvokeFinalizeHook(header, state, true); len(otsTxs) > 0 {
    for _, tx := range otsTxs {
        if err := p.applyOTSTransaction(tx, state, header, cx, &body.Transactions, &receipts, &header.GasUsed, tracer); err != nil {
            log.Error("OTS: Failed to apply OTS transaction", "err", err)
            // Fail-Open: continue even if OTS transaction fails
        }
    }
}
```

#### 1.3 添加 applyOTSTransaction 和 applyOTSMessage 函数

在文件末尾（或 `applyTransaction` 函数附近）添加：

```go
// applyOTSTransaction applies an OTS system transaction to the state.
// OTS transactions have gasPrice=0 and are executed by coinbase.
func (p *Parlia) applyOTSTransaction(
	tx *types.Transaction,
	state *state.StateDB,
	header *types.Header,
	chainContext core.ChainContext,
	txs *[]*types.Transaction,
	receipts *[]*types.Receipt,
	usedGas *uint64,
	tracer *tracing.Hooks,
) error {
	// Verify this is a valid OTS system transaction
	if tx.GasPrice().Sign() != 0 {
		return errors.New("OTS transaction must have zero gas price")
	}

	// Sign the transaction with coinbase
	signedTx, err := p.signTxFn(accounts.Account{Address: p.val}, tx, p.chainConfig.ChainID)
	if err != nil {
		return fmt.Errorf("failed to sign OTS transaction: %w", err)
	}

	// Set transaction context
	state.SetTxContext(signedTx.Hash(), len(*txs))

	// Create EVM context (following BSC's pattern for system transactions)
	blockContext := core.NewEVMBlockContext(header, chainContext, nil)
	evm := vm.NewEVM(blockContext, state, p.chainConfig, vm.Config{Tracer: tracer})
	evm.SetTxContext(core.NewEVMTxContext(&core.Message{
		From:      p.val,
		GasPrice:  common.Big0,
		GasFeeCap: common.Big0,
		GasTipCap: common.Big0,
	}))

	// Prepare state
	if p.chainConfig.IsCancun(header.Number, header.Time) {
		rules := evm.ChainConfig().Rules(header.Number, header.Time >= 0, header.Time)
		state.Prepare(rules, p.val, header.Coinbase, tx.To(), vm.ActivePrecompiles(rules), tx.AccessList())
	}

	// Increment nonce
	state.SetNonce(p.val, state.GetNonce(p.val)+1, tracing.NonceChangeEoACall)

	// Execute the transaction
	gasUsed, err := p.applyOTSMessage(evm, &core.Message{
		From:     p.val,
		To:       tx.To(),
		Value:    tx.Value(),
		GasLimit: tx.Gas(),
		GasPrice: common.Big0,
		Data:     tx.Data(),
	}, state, header)
	if err != nil {
		log.Error("OTS: Transaction execution failed", "err", err)
		// Continue with failed receipt for transparency
	}

	// Create receipt
	receipt := &types.Receipt{
		Type:              tx.Type(),
		TxHash:            signedTx.Hash(),
		GasUsed:           gasUsed,
		BlockHash:         header.Hash(),
		BlockNumber:       header.Number,
		TransactionIndex:  uint(len(*txs)),
		ContractAddress:   common.Address{},
		Logs:              state.GetLogs(signedTx.Hash(), header.Number.Uint64(), header.Hash()),
		CumulativeGasUsed: *usedGas + gasUsed,
	}
	if err != nil {
		receipt.Status = types.ReceiptStatusFailed
	} else {
		receipt.Status = types.ReceiptStatusSuccessful
	}

	// Update state
	*txs = append(*txs, signedTx)
	*receipts = append(*receipts, receipt)
	*usedGas += gasUsed

	log.Debug("OTS: Transaction applied",
		"txHash", signedTx.Hash().Hex(),
		"gasUsed", gasUsed,
		"status", receipt.Status,
	)

	return nil
}

// applyOTSMessage executes an OTS message against the state
func (p *Parlia) applyOTSMessage(
	evm *vm.EVM,
	msg *core.Message,
	state *state.StateDB,
	header *types.Header,
) (uint64, error) {
	ret, returnGas, err := evm.Call(
		vm.AccountRef(msg.From),
		*msg.To,
		msg.Data,
		msg.GasLimit,
		uint256.MustFromBig(msg.Value),
	)
	if err != nil {
		log.Error("OTS: Message execution failed", "ret", string(ret), "err", err)
	}
	return msg.GasLimit - returnGas, err
}
```

---

## 2. eth/backend.go

### 目的
在以太坊后端启动时初始化 OTS 模块，使 OTS 功能在节点启动时自动加载。

### 修改位置
文件顶部 import 区域 + `Ethereum` 结构体 + `New()` 函数

### 具体修改

#### 2.1 添加 Import

```go
import (
    // ... 现有 imports ...
    "github.com/ethereum/go-ethereum/ots"  // 添加此行
)
```

#### 2.2 在 Ethereum 结构体中添加字段

在 `Ethereum` 结构体中添加：

```go
type Ethereum struct {
    // ... 现有字段 ...

    votePool  *vote.VotePool
    stopCh    chan struct{}
    otsModule *ots.Module // OTS module for OpenTimestamps integration  // 添加此行
}
```

#### 2.3 在 New() 函数末尾添加 OTS 初始化

在 `New()` 函数的 `return eth, nil` 之前添加：

```go
// Initialize OTS module if configured
if config.OTS.Enabled {
    otsConfig := ots.DefaultConfig()
    otsConfig.Enabled = true
    otsConfig.Mode = ots.Mode(config.OTS.Mode)
    otsConfig.DataDir = stack.ResolvePath(config.OTS.DataDir)
    if config.OTS.ContractAddress != "" {
        otsConfig.ContractAddress = common.HexToAddress(config.OTS.ContractAddress)
    }
    otsConfig.TriggerHour = config.OTS.TriggerHour
    otsConfig.FallbackBlocks = config.OTS.FallbackBlocks
    otsConfig.Confirmations = config.OTS.Confirmations

    otsModule, err := ots.NewModule(otsConfig, eth.blockchain, eth.chainDb)
    if err != nil {
        log.Error("OTS: Failed to create module", "err", err)
    } else {
        eth.otsModule = otsModule
        if err := otsModule.Start(); err != nil {
            log.Error("OTS: Failed to start module", "err", err)
        } else {
            log.Info("OTS: Module started successfully", "mode", otsConfig.Mode)
        }
    }
}

return eth, nil
```

---

## 3. eth/ethconfig/config.go

### 目的
定义 OTS 配置结构体，使 OTS 可以通过 config.toml 进行配置。

### 修改位置
在 `FullNodeGPO` 变量之后，`Defaults` 变量之前

### 具体修改

#### 3.1 添加 OTSConfig 结构体

```go
// OTSConfig contains configuration for the OTS (OpenTimestamps) module.
// This module provides Bitcoin-anchored timestamping for RMC blockchain events.
type OTSConfig struct {
    // Enabled activates the OTS module
    Enabled bool `toml:",omitempty"`

    // Mode specifies the operation mode: "producer", "watcher", or "full"
    // - producer: Creates batches and submits to OTS calendars
    // - watcher: Only monitors and verifies OTS proofs
    // - full: Both producer and watcher functionality
    Mode string `toml:",omitempty"`

    // DataDir is the directory for OTS data storage (relative to node datadir)
    DataDir string `toml:",omitempty"`

    // ContractAddress is the OTSAnchor contract address
    ContractAddress string `toml:",omitempty"`

    // CalendarURLs are the OTS calendar server URLs
    CalendarURLs []string `toml:",omitempty"`

    // TriggerHour is the UTC hour for daily batch trigger (0-23)
    TriggerHour uint8 `toml:",omitempty"`

    // FallbackBlocks is the number of blocks before fallback trigger
    FallbackBlocks uint64 `toml:",omitempty"`

    // MaxRetries is the maximum number of OTS submission retries
    MaxRetries uint32 `toml:",omitempty"`

    // Confirmations is the number of block confirmations before processing
    Confirmations uint64 `toml:",omitempty"`
}

// DefaultOTSConfig returns the default OTS configuration (disabled by default)
var DefaultOTSConfig = OTSConfig{
    Enabled:        false,
    Mode:           "full",
    DataDir:        "ots",
    TriggerHour:    0,
    FallbackBlocks: 28800, // ~1 day at 3s blocks
    MaxRetries:     3,
    Confirmations:  15, // Block confirmations before processing
    CalendarURLs: []string{
        "https://alice.btc.calendar.opentimestamps.org",
        "https://bob.btc.calendar.opentimestamps.org",
        "https://finney.calendar.eternitywall.com",
    },
}
```

#### 3.2 在 Defaults 变量中添加 OTS

```go
var Defaults = Config{
    // ... 现有配置 ...
    BlobExtraReserve:   params.DefaultExtraReserveForBlobRequests,
    OTS:                DefaultOTSConfig,  // 添加此行
}
```

#### 3.3 在 Config 结构体中添加 OTS 字段

```go
type Config struct {
    // ... 现有字段 ...

    // blob setting
    BlobExtraReserve uint64

    // OTS module configuration
    OTS OTSConfig  // 添加此行
}
```

---

## 4. eth/ethconfig/gen_config.go

### 目的
为 OTSConfig 添加 TOML 序列化/反序列化支持。

> 注意: 此文件通常由 `go generate` 自动生成，但需要手动添加 OTS 支持。

### 具体修改

#### 4.1 在 MarshalTOML 的 Config 结构体中添加

```go
type Config struct {
    // ... 现有字段 ...
    BlobExtraReserve        uint64
    OTS                     OTSConfig  // 添加此行
}
```

#### 4.2 在 MarshalTOML 的编码部分添加

```go
enc.BlobExtraReserve = c.BlobExtraReserve
enc.OTS = c.OTS  // 添加此行
return &enc, nil
```

#### 4.3 在 UnmarshalTOML 的 Config 结构体中添加

```go
type Config struct {
    // ... 现有字段 ...
    BlobExtraReserve        *uint64
    OTS                     *OTSConfig  // 添加此行
}
```

#### 4.4 在 UnmarshalTOML 的解码部分添加

```go
if dec.BlobExtraReserve != nil {
    c.BlobExtraReserve = *dec.BlobExtraReserve
}
if dec.OTS != nil {  // 添加以下3行
    c.OTS = *dec.OTS
}
return nil
```

---

## 5. internal/flags/categories.go

### 目的
添加 OTS 命令行参数分类，使 `--help` 输出更清晰。

### 具体修改

在 const 块中添加：

```go
const (
    // ... 现有分类 ...
    FastFinalityCategory = "FAST FINALITY"
    BlockHistoryCategory = "BLOCK HISTORY MANAGEMENT"
    OTSCategory          = "OTS (OPENTIMESTAMPS)"  // 添加此行
)
```

---

## 6. cmd/utils/flags.go

### 目的
定义 OTS 相关的 CLI flags，使用户可以通过命令行配置 OTS。

### 修改位置
1. 变量定义区域（在其他 flags 定义之后）
2. 变量分组区域（在 DatabaseFlags 之后）
3. SetEthConfig 函数（在函数末尾）

### 具体修改

#### 6.1 添加 OTS Flags 定义

在变量定义区域（约在 `FakeBeaconPortFlag` 之后）添加：

```go
// OTS (OpenTimestamps) flags
OTSEnabledFlag = &cli.BoolFlag{
    Name:     "ots.enabled",
    Usage:    "Enable OTS (OpenTimestamps) module for Bitcoin-anchored timestamping",
    Category: flags.OTSCategory,
}
OTSModeFlag = &cli.StringFlag{
    Name:     "ots.mode",
    Usage:    "OTS operation mode: producer, watcher, or full",
    Value:    "full",
    Category: flags.OTSCategory,
}
OTSDataDirFlag = &cli.StringFlag{
    Name:     "ots.datadir",
    Usage:    "Data directory for OTS storage (relative to node datadir)",
    Value:    "ots",
    Category: flags.OTSCategory,
}
OTSContractFlag = &cli.StringFlag{
    Name:     "ots.contract",
    Usage:    "OTSAnchor contract address",
    Value:    "0x0000000000000000000000000000000000001001",
    Category: flags.OTSCategory,
}
OTSTriggerHourFlag = &cli.UintFlag{
    Name:     "ots.trigger-hour",
    Usage:    "UTC hour for daily batch trigger (0-23)",
    Value:    0,
    Category: flags.OTSCategory,
}
OTSFallbackBlocksFlag = &cli.Uint64Flag{
    Name:     "ots.fallback-blocks",
    Usage:    "Number of blocks before fallback trigger",
    Value:    28800,
    Category: flags.OTSCategory,
}
OTSConfirmationsFlag = &cli.Uint64Flag{
    Name:     "ots.confirmations",
    Usage:    "Block confirmations before processing",
    Value:    15,
    Category: flags.OTSCategory,
}
OTSMaxRetriesFlag = &cli.UintFlag{
    Name:     "ots.max-retries",
    Usage:    "Maximum OTS submission retries",
    Value:    3,
    Category: flags.OTSCategory,
}
```

#### 6.2 添加 OTSFlags 分组

在 `DatabaseFlags` 分组之后添加：

```go
// OTSFlags is the flag group of all OTS (OpenTimestamps) flags.
OTSFlags = []cli.Flag{
    OTSEnabledFlag,
    OTSModeFlag,
    OTSDataDirFlag,
    OTSContractFlag,
    OTSTriggerHourFlag,
    OTSFallbackBlocksFlag,
    OTSConfirmationsFlag,
    OTSMaxRetriesFlag,
}
```

#### 6.3 在 SetEthConfig 函数末尾添加 OTS 处理

在 `SetEthConfig` 函数的末尾（`}` 之前）添加：

```go
// OTS (OpenTimestamps) config
if ctx.IsSet(OTSEnabledFlag.Name) {
    cfg.OTS.Enabled = ctx.Bool(OTSEnabledFlag.Name)
}
if ctx.IsSet(OTSModeFlag.Name) {
    cfg.OTS.Mode = ctx.String(OTSModeFlag.Name)
}
if ctx.IsSet(OTSDataDirFlag.Name) {
    cfg.OTS.DataDir = ctx.String(OTSDataDirFlag.Name)
}
if ctx.IsSet(OTSContractFlag.Name) {
    cfg.OTS.ContractAddress = ctx.String(OTSContractFlag.Name)
}
if ctx.IsSet(OTSTriggerHourFlag.Name) {
    cfg.OTS.TriggerHour = uint8(ctx.Uint(OTSTriggerHourFlag.Name))
}
if ctx.IsSet(OTSFallbackBlocksFlag.Name) {
    cfg.OTS.FallbackBlocks = ctx.Uint64(OTSFallbackBlocksFlag.Name)
}
if ctx.IsSet(OTSConfirmationsFlag.Name) {
    cfg.OTS.Confirmations = ctx.Uint64(OTSConfirmationsFlag.Name)
}
if ctx.IsSet(OTSMaxRetriesFlag.Name) {
    cfg.OTS.MaxRetries = uint32(ctx.Uint(OTSMaxRetriesFlag.Name))
}
```

---

## 7. cmd/geth/main.go

### 目的
将 OTS flags 注册到 geth 主程序，使 `geth --help` 显示 OTS 选项。

### 具体修改

在 `init()` 函数中，找到 `app.Flags = slices.Concat(...)` 部分，添加 `utils.OTSFlags`：

```go
app.Flags = slices.Concat(
    nodeFlags,
    rpcFlags,
    consoleFlags,
    debug.Flags,
    metricsFlags,
    fakeBeaconFlags,
    utils.OTSFlags,  // 添加此行
)
```

---

## 验证清单

完成所有修改后，执行以下验证步骤：

### 1. 编译验证

```bash
cd rmc
make geth
```

### 2. CLI 帮助验证

```bash
./build/bin/geth --help | grep -A10 "OTS (OPENTIMESTAMPS)"
```

预期输出：
```
OTS (OPENTIMESTAMPS):
  --ots.enabled              Enable OTS module for Bitcoin-anchored timestamping
  --ots.mode value           OTS operation mode: producer, watcher, or full (default: "full")
  --ots.contract value       OTSAnchor contract address
  --ots.trigger-hour value   UTC hour for daily batch trigger (default: 0)
  --ots.fallback-blocks value Number of blocks before fallback trigger (default: 28800)
  --ots.confirmations value  Block confirmations before processing (default: 15)
  --ots.max-retries value    Maximum OTS submission retries (default: 3)
```

### 3. 配置验证

创建测试 config.toml：

```toml
[Eth.OTS]
Enabled = true
Mode = "full"
ContractAddress = "0x0000000000000000000000000000000000001001"
```

启动节点验证日志输出包含：
```
OTS: Module started successfully    mode=full
```

---

## BSC 升级同步流程

当 BSC 上游发布新版本时，按以下步骤同步：

1. **备份当前修改**
   ```bash
   git stash  # 或创建备份分支
   ```

2. **合并上游代码**
   ```bash
   git fetch upstream
   git merge upstream/master
   ```

3. **解决冲突**
   - 检查本文档中列出的7个文件
   - 按照本文档重新应用修改
   - 注意 BSC 可能修改了函数签名或结构体字段

4. **验证编译**
   ```bash
   make geth
   ```

5. **运行测试**
   ```bash
   make test
   ./build/bin/geth --help | grep ots
   ```

---

## 修改历史

| 日期 | 版本 | 修改内容 |
|------|------|---------|
| 2024-XX-XX | v1.0 | 初始 OTS 集成 |

---

## 联系方式

如有问题，请联系 RMC 开发团队。
