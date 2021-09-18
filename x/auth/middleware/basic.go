package middleware

import (
	"context"

	"github.com/cosmos/cosmos-sdk/codec/legacy"
	"github.com/cosmos/cosmos-sdk/crypto/keys/multisig"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/cosmos/cosmos-sdk/types/tx"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/cosmos/cosmos-sdk/x/auth/migrations/legacytx"
	authsigning "github.com/cosmos/cosmos-sdk/x/auth/signing"
	abci "github.com/tendermint/tendermint/abci/types"
)

// ValidateBasicMiddleware will call tx.ValidateBasic, msg.ValidateBasic(for each msg inside tx)
// and return any non-nil error.
// If ValidateBasic passes, middleware calls next middleware in chain. Note,
// validateBasicMiddleware will not get executed on ReCheckTx since it
// is not dependent on application state.
type validateBasicMiddleware struct {
	next tx.Handler
}

func ValidateBasicMiddleware(txh tx.Handler) tx.Handler {
	return validateBasicMiddleware{
		next: txh,
	}
}

var _ tx.Handler = validateBasicMiddleware{}

// CheckTx implements tx.Handler.CheckTx.
func (basic validateBasicMiddleware) CheckTx(ctx context.Context, tx sdk.Tx, req abci.RequestCheckTx) (abci.ResponseCheckTx, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// no need to validate basic on recheck tx, call next middleware
	if sdkCtx.IsReCheckTx() {
		return basic.next.CheckTx(ctx, tx, req)
	}

	if err := tx.ValidateBasic(); err != nil {
		return abci.ResponseCheckTx{}, err
	}

	return basic.next.CheckTx(ctx, tx, req)
}

// DeliverTx implements tx.Handler.DeliverTx.
func (basic validateBasicMiddleware) DeliverTx(ctx context.Context, tx sdk.Tx, req abci.RequestDeliverTx) (abci.ResponseDeliverTx, error) {
	if err := tx.ValidateBasic(); err != nil {
		return abci.ResponseDeliverTx{}, err
	}

	return basic.next.DeliverTx(ctx, tx, req)
}

// SimulateTx implements tx.Handler.SimulateTx.
func (basic validateBasicMiddleware) SimulateTx(ctx context.Context, sdkTx sdk.Tx, req tx.RequestSimulateTx) (tx.ResponseSimulateTx, error) {
	if err := sdkTx.ValidateBasic(); err != nil {
		return tx.ResponseSimulateTx{}, err
	}

	return basic.next.SimulateTx(ctx, sdkTx, req)
}

var _ tx.Handler = txTimeoutHeightMiddleware{}

type (
	// TxTimeoutHeightMiddleware defines a middleware that checks for a
	// tx height timeout.
	txTimeoutHeightMiddleware struct {
		next tx.Handler
	}

	// TxWithTimeoutHeight defines the interface a tx must implement in order for
	// TxHeightTimeoutMiddleware to process the tx.
	TxWithTimeoutHeight interface {
		sdk.Tx

		GetTimeoutHeight() uint64
	}
)

// TxTimeoutHeightMiddleware defines a middleware that checks for a
// tx height timeout.
func TxTimeoutHeightMiddleware(txh tx.Handler) tx.Handler {
	return txTimeoutHeightMiddleware{
		next: txh,
	}
}

func checkTimeout(ctx context.Context, tx sdk.Tx) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	timeoutTx, ok := tx.(TxWithTimeoutHeight)
	if !ok {
		return sdkerrors.Wrap(sdkerrors.ErrTxDecode, "expected tx to implement TxWithTimeoutHeight")
	}

	timeoutHeight := timeoutTx.GetTimeoutHeight()
	if timeoutHeight > 0 && uint64(sdkCtx.BlockHeight()) > timeoutHeight {
		return sdkerrors.Wrapf(
			sdkerrors.ErrTxTimeoutHeight, "block height: %d, timeout height: %d", sdkCtx.BlockHeight(), timeoutHeight,
		)
	}

	return nil
}

// CheckTx implements tx.Handler.CheckTx.
func (txh txTimeoutHeightMiddleware) CheckTx(ctx context.Context, tx sdk.Tx, req abci.RequestCheckTx) (abci.ResponseCheckTx, error) {
	if err := checkTimeout(ctx, tx); err != nil {
		return abci.ResponseCheckTx{}, err
	}

	return txh.next.CheckTx(ctx, tx, req)
}

// DeliverTx implements tx.Handler.DeliverTx.
func (txh txTimeoutHeightMiddleware) DeliverTx(ctx context.Context, tx sdk.Tx, req abci.RequestDeliverTx) (abci.ResponseDeliverTx, error) {
	if err := checkTimeout(ctx, tx); err != nil {
		return abci.ResponseDeliverTx{}, err
	}

	return txh.next.DeliverTx(ctx, tx, req)
}

// SimulateTx implements tx.Handler.SimulateTx.
func (txh txTimeoutHeightMiddleware) SimulateTx(ctx context.Context, sdkTx sdk.Tx, req tx.RequestSimulateTx) (tx.ResponseSimulateTx, error) {
	if err := checkTimeout(ctx, sdkTx); err != nil {
		return tx.ResponseSimulateTx{}, err
	}

	return txh.next.SimulateTx(ctx, sdkTx, req)
}

// validateMemoMiddleware will validate memo given the parameters passed in
// If memo is too large middleware returns with error, otherwise call next middleware
// CONTRACT: Tx must implement TxWithMemo interface
type validateMemoMiddleware struct {
	ak   AccountKeeper
	next tx.Handler
}

func ValidateMemoMiddleware(ak AccountKeeper) tx.Middleware {
	return func(txHandler tx.Handler) tx.Handler {
		return validateMemoMiddleware{
			ak:   ak,
			next: txHandler,
		}
	}
}

var _ tx.Handler = validateMemoMiddleware{}

func (vmd validateMemoMiddleware) checkForValidMemo(ctx context.Context, tx sdk.Tx) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	memoTx, ok := tx.(sdk.TxWithMemo)
	if !ok {
		return sdkerrors.Wrap(sdkerrors.ErrTxDecode, "invalid transaction type")
	}

	params := vmd.ak.GetParams(sdkCtx)

	memoLength := len(memoTx.GetMemo())
	if uint64(memoLength) > params.MaxMemoCharacters {
		return sdkerrors.Wrapf(sdkerrors.ErrMemoTooLarge,
			"maximum number of characters is %d but received %d characters",
			params.MaxMemoCharacters, memoLength,
		)
	}

	return nil
}

// CheckTx implements tx.Handler.CheckTx method.
func (vmd validateMemoMiddleware) CheckTx(ctx context.Context, tx sdk.Tx, req abci.RequestCheckTx) (abci.ResponseCheckTx, error) {
	if err := vmd.checkForValidMemo(ctx, tx); err != nil {
		return abci.ResponseCheckTx{}, err
	}

	return vmd.next.CheckTx(ctx, tx, req)
}

// DeliverTx implements tx.Handler.DeliverTx method.
func (vmd validateMemoMiddleware) DeliverTx(ctx context.Context, tx sdk.Tx, req abci.RequestDeliverTx) (abci.ResponseDeliverTx, error) {
	if err := vmd.checkForValidMemo(ctx, tx); err != nil {
		return abci.ResponseDeliverTx{}, err
	}

	return vmd.next.DeliverTx(ctx, tx, req)
}

// SimulateTx implements tx.Handler.SimulateTx method.
func (vmd validateMemoMiddleware) SimulateTx(ctx context.Context, sdkTx sdk.Tx, req tx.RequestSimulateTx) (tx.ResponseSimulateTx, error) {
	if err := vmd.checkForValidMemo(ctx, sdkTx); err != nil {
		return tx.ResponseSimulateTx{}, err
	}

	return vmd.next.SimulateTx(ctx, sdkTx, req)
}

var _ tx.Handler = consumeTxSizeGasMiddleware{}

// consumeTxSizeGasMiddleware will take in parameters and consume gas proportional
// to the size of tx before calling next middleware. Note, the gas costs will be
// slightly over estimated due to the fact that any given signing account may need
// to be retrieved from state.
//
// CONTRACT: If simulate=true, then signatures must either be completely filled
// in or empty.
// CONTRACT: To use this middleware, signatures of transaction must be represented
// as legacytx.StdSignature otherwise simulate mode will incorrectly estimate gas cost.
type consumeTxSizeGasMiddleware struct {
	ak   AccountKeeper
	next tx.Handler
}

func ConsumeTxSizeGasMiddleware(ak AccountKeeper) tx.Middleware {
	return func(txHandler tx.Handler) tx.Handler {
		return consumeTxSizeGasMiddleware{
			ak:   ak,
			next: txHandler,
		}
	}
}

// CheckTx implements tx.Handler.CheckTx.
func (cgts consumeTxSizeGasMiddleware) CheckTx(ctx context.Context, tx sdk.Tx, req abci.RequestCheckTx) (abci.ResponseCheckTx, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	_, ok := tx.(authsigning.SigVerifiableTx)
	if !ok {
		return abci.ResponseCheckTx{}, sdkerrors.Wrap(sdkerrors.ErrTxDecode, "invalid tx type")
	}
	params := cgts.ak.GetParams(sdkCtx)
	sdkCtx.GasMeter().ConsumeGas(params.TxSizeCostPerByte*sdk.Gas(len(sdkCtx.TxBytes())), "txSize")

	return cgts.next.CheckTx(ctx, tx, req)
}

// DeliverTx implements tx.Handler.DeliverTx.
func (cgts consumeTxSizeGasMiddleware) DeliverTx(ctx context.Context, tx sdk.Tx, req abci.RequestDeliverTx) (abci.ResponseDeliverTx, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	_, ok := tx.(authsigning.SigVerifiableTx)
	if !ok {
		return abci.ResponseDeliverTx{}, sdkerrors.Wrap(sdkerrors.ErrTxDecode, "invalid tx type")
	}
	params := cgts.ak.GetParams(sdkCtx)
	sdkCtx.GasMeter().ConsumeGas(params.TxSizeCostPerByte*sdk.Gas(len(sdkCtx.TxBytes())), "txSize")

	return cgts.next.DeliverTx(ctx, tx, req)
}

// SimulateTx implements tx.Handler.SimulateTx.
func (cgts consumeTxSizeGasMiddleware) SimulateTx(ctx context.Context, sdkTx sdk.Tx, req tx.RequestSimulateTx) (tx.ResponseSimulateTx, error) {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	sigTx, ok := sdkTx.(authsigning.SigVerifiableTx)
	if !ok {
		return tx.ResponseSimulateTx{}, sdkerrors.Wrap(sdkerrors.ErrTxDecode, "invalid tx type")
	}
	params := cgts.ak.GetParams(sdkCtx)
	sdkCtx.GasMeter().ConsumeGas(params.TxSizeCostPerByte*sdk.Gas(len(sdkCtx.TxBytes())), "txSize")

	// simulate gas cost for signatures in simulate mode
	// in simulate mode, each element should be a nil signature
	sigs, err := sigTx.GetSignaturesV2()
	if err != nil {
		return tx.ResponseSimulateTx{}, err
	}
	n := len(sigs)

	for i, signer := range sigTx.GetSigners() {
		// if signature is already filled in, no need to simulate gas cost
		if i < n && !isIncompleteSignature(sigs[i].Data) {
			continue
		}

		var pubkey cryptotypes.PubKey

		acc := cgts.ak.GetAccount(sdkCtx, signer)

		// use placeholder simSecp256k1Pubkey if sig is nil
		if acc == nil || acc.GetPubKey() == nil {
			pubkey = simSecp256k1Pubkey
		} else {
			pubkey = acc.GetPubKey()
		}

		// use stdsignature to mock the size of a full signature
		simSig := legacytx.StdSignature{ //nolint:staticcheck // this will be removed when proto is ready
			Signature: simSecp256k1Sig[:],
			PubKey:    pubkey,
		}

		sigBz := legacy.Cdc.MustMarshal(simSig)
		cost := sdk.Gas(len(sigBz) + 6)

		// If the pubkey is a multi-signature pubkey, then we estimate for the maximum
		// number of signers.
		if _, ok := pubkey.(*multisig.LegacyAminoPubKey); ok {
			cost *= params.TxSigLimit
		}

		sdkCtx.GasMeter().ConsumeGas(params.TxSizeCostPerByte*cost, "txSize")
	}

	return cgts.next.SimulateTx(ctx, sdkTx, req)
}

// isIncompleteSignature tests whether SignatureData is fully filled in for simulation purposes
func isIncompleteSignature(data signing.SignatureData) bool {
	if data == nil {
		return true
	}

	switch data := data.(type) {
	case *signing.SingleSignatureData:
		return len(data.Signature) == 0
	case *signing.MultiSignatureData:
		if len(data.Signatures) == 0 {
			return true
		}
		for _, s := range data.Signatures {
			if isIncompleteSignature(s) {
				return true
			}
		}
	}

	return false
}