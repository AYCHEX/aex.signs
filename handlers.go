package main

import (
	// "encoding/hex"
	jwt "github.com/dgrijalva/jwt-go"
	"github.com/go-chi/jwtauth"
	"github.com/go-chi/render"
	"net/http"

	"encoding/json"
	"errors"
	sdk "github.com/binance-chain/go-sdk/client"
	"github.com/binance-chain/go-sdk/keys"
	"github.com/binance-chain/go-sdk/common/types"
	"github.com/binance-chain/go-sdk/types/tx"
)

// Error responses

type Response struct {
	Response string
}

type WalletResponse struct {
	Name    string
	Address string
}

type WalletsResponse struct {
	Wallets []WalletResponse
}

type BroadcastResult struct {
	Ok   bool
	Hash string
	Data string
}

type BroadcastResponse struct {
	Results []BroadcastResult
}

func BroadcastResultFromTxCommitResult(result tx.TxCommitResult) BroadcastResult {
	return BroadcastResult{
		Ok:   result.Ok,
		Hash: result.Hash,
		Data: result.Data,
	}
}

func BroadcastResponseFromTxCommitResults(results []tx.TxCommitResult) BroadcastResponse {
	var r BroadcastResponse
	for _, res := range results {
		br := BroadcastResultFromTxCommitResult(res)
		r.Results = append(r.Results, br)
	}
	return r
}

type ErrResponse struct {
	Err            error `json:"-"` // low-level runtime error
	HTTPStatusCode int   `json:"-"` // http response status code

	StatusText string `json:"status"`          // user-level status message
	AppCode    int64  `json:"code,omitempty"`  // application-specific error code
	ErrorText  string `json:"error,omitempty"` // application-level error message, for debugging
}

func (e *ErrResponse) Render(w http.ResponseWriter, r *http.Request) error {
	render.Status(r, e.HTTPStatusCode)
	return nil
}

func ErrPermissionDenied() render.Renderer {
	return &ErrResponse{
		Err:            nil,
		HTTPStatusCode: 403,
		StatusText:     "Permission denied.",
		ErrorText:      "",
	}
}

func ErrInvalidRequest(err error) render.Renderer {
	return &ErrResponse{
		Err:            err,
		HTTPStatusCode: 400,
		StatusText:     "Invalid request.",
		ErrorText:      err.Error(),
	}
}

// Utility functions

func WriteResponse(w http.ResponseWriter, r *http.Request, result string) {
	WriteJSONResponse(w, r, Response{Response: result})
}

func WriteJSONResponse(w http.ResponseWriter, r *http.Request, result interface{}) {
	j, err := json.Marshal(result)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	w.Write(j)
}

func decodeSignedMessage(r *http.Request, payload interface{}) error {
	token, _, err := jwtauth.FromContext(r.Context())
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return errors.New("Failed to get JWT claims.")
	}

	err = json.Unmarshal([]byte(claims["payload"].(string)), payload)
	if err != nil {
		return err
	}

	return nil
}

func decodePayload(r *http.Request, payload interface{}) error {
	token, _, err := jwtauth.FromContext(r.Context())
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return errors.New("Failed to get JWT claims.")
	}

	err = json.Unmarshal([]byte(claims["payload"].(string)), payload)
	if err != nil {
		return err
	}

	return nil
}

func decodeRequest(r *http.Request, payload interface{}, action Permission) (*DexVaultDatastore, string, keys.KeyManager, error) {
	err := decodePayload(r, payload)
	if err != nil {
		return nil, "", nil, err
	}

	datastore := GetRequestDatastore(r)
	user := GetRequestUser(r)

	if datastore == nil {
		return nil, "", nil, errors.New("No datastore could be found.")
	}
	if user == "" {
		return nil, "", nil, errors.New("No user could be found.")
	}

	basicMessage := &BasicMessage{}
	err = decodeSignedMessage(r, basicMessage)
	if err != nil {
		return nil, "", nil, errors.New("Failed to decode signed message")
	}

	// Also check permissions
	if !datastore.IsPermitted(user, basicMessage.Wallet, action) {
		return nil, "", nil, errors.New("Not permitted.")
	}

	wallet := datastore.GetWallet(basicMessage.Wallet)
	if wallet == nil {
		return nil, "", nil, errors.New("No matching wallet could be found.")
	}

	keyManager, err := wallet.GetKeyManager()
	if err != nil {
		return nil, "", nil, err
	}

	return datastore, user, keyManager, nil
}

// Handlers

func decodeRequestBasic(r *http.Request, payload interface{}) (*DexVaultDatastore, string, error) {
	err := decodePayload(r, payload)
	if err != nil {
		return nil, "", err
	}

	datastore := GetRequestDatastore(r)
	user := GetRequestUser(r)

	if datastore == nil {
		return nil, "", errors.New("No datastore could be found.")
	}
	if user == "" {
		return nil, "", errors.New("No user could be found.")
	}

	return datastore, user, nil
}

func broadcastMessage(keyManager keys.KeyManager, host string, network int, tx []byte) (*BroadcastResponse, error) {
	client, err := sdk.NewDexClient("testnet-dex.binance.org", types.ChainNetwork(network), keyManager)
	if err != nil {
		return nil, err
	}

	param := map[string]string{}
	param["sync"] = "true"
	commits, err := client.PostTx([]byte(tx), param)

	if err != nil {
		return nil, err
	}

	response := BroadcastResponseFromTxCommitResults(commits)
	return &response, err
}

func createWalletHandler(w http.ResponseWriter, r *http.Request) {
	data := &BasicMessage{}
	datastore, user, err := decodeRequestBasic(r, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	u := datastore.GetUser(user)

	if !u.HasPermission(PermissionCreateWallet) {
		render.Render(w, r, ErrPermissionDenied())
		return
	}

	wallet, err := datastore.CreateWallet(data.Wallet)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	address, err := wallet.GetAddress()
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	WriteResponse(w, r, *address)
}

func getAddressHandler(w http.ResponseWriter, r *http.Request) {
	data := &Wallet{}
	datastore, user, keyManager, err := decodeRequest(r, data, PermissionRead)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	WriteResponse(w, r, keyManager.GetAddr().String())
}

func getWalletHandler(w http.ResponseWriter, r *http.Request) {
	data := &Wallet{}
	datastore, user, keyManager, err := decodeRequest(r, data, PermissionRead)
	_ = datastore
	_ = user
	_ = keyManager
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	wa, err := data.GetAddress()
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	wr := WalletResponse{
		Name:    data.Name,
		Address: *wa,
	}

	WriteJSONResponse(w, r, wr)
}

func getWalletsHandler(w http.ResponseWriter, r *http.Request) {
	datastore := GetRequestDatastore(r)
	user := GetRequestUser(r)
	u := datastore.GetUser(user)
	if !u.HasPermission(PermissionRead) {
		render.Render(w, r, ErrPermissionDenied())
		return
	}

	wrs := WalletsResponse{}
	for _, wallet := range datastore.Wallets {
		wa, err := wallet.GetAddress()
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}

		wr := WalletResponse{
			Name:    wallet.Name,
			Address: *wa,
		}

		wrs.Wallets = append(wrs.Wallets, wr)
	}

	WriteJSONResponse(w, r, wrs)
}

// These handlers are separate functions. This is done
// to be able to add more validation etc functionality
// later on.

func createOrderHandler(w http.ResponseWriter, r *http.Request) {
	data := &CreateOrder{}
	datastore, user, keyManager, err := decodeRequest(r, data, PermissionCreateOrder)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedCreateOrderMessage(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func cancelOrderHandler(w http.ResponseWriter, r *http.Request) {
	data := &CancelOrder{}
	datastore, user, keyManager, err := decodeRequest(r, data, PermissionCancelOrder)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedCancelOrderMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func tokenBurnHandler(w http.ResponseWriter, r *http.Request) {
	data := &TokenBurn{}
	datastore, user, keyManager, err := decodeRequest(r, data, PermissionTokenBurn)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedTokenBurnMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func depositHandler(w http.ResponseWriter, r *http.Request) {
	data := &DepositProposal{}
	datastore, user, keyManager, err := decodeRequest(r, data, PermissionDeposit)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedDepositMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func freezeTokenHandler(w http.ResponseWriter, r *http.Request) {
	data := &FreezeToken{}

	datastore, user, keyManager, err := decodeRequest(r, data, PermissionFreezeToken)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedFreezeTokenMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func issueTokenHandler(w http.ResponseWriter, r *http.Request) {
	data := &IssueToken{}

	datastore, user, keyManager, err := decodeRequest(r, data, PermissionIssueToken)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedIssueTokenMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func listPairHandler(w http.ResponseWriter, r *http.Request) {
	data := &ListPair{}

	datastore, user, keyManager, err := decodeRequest(r, data, PermissionListPair)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedListPairMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func mintTokenHandler(w http.ResponseWriter, r *http.Request) {
	data := &MintToken{}

	datastore, user, keyManager, err := decodeRequest(r, data, PermissionMintToken)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedMintTokenMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func sendTokenHandler(w http.ResponseWriter, r *http.Request) {
	data := &SendToken{}

	datastore, user, keyManager, err := decodeRequest(r, data, PermissionSendToken)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedSendTokenMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func submitProposalHandler(w http.ResponseWriter, r *http.Request) {
	data := &SubmitProposal{}

	datastore, user, keyManager, err := decodeRequest(r, data, PermissionSubmitProposal)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedSubmitProposalMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func unfreezeTokenHandler(w http.ResponseWriter, r *http.Request) {
	data := &UnfreezeToken{}

	datastore, user, keyManager, err := decodeRequest(r, data, PermissionUnfreezeToken)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createUnfreezeTokenMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}

func voteProposalHandler(w http.ResponseWriter, r *http.Request) {
	data := &VoteProposal{}

	datastore, user, keyManager, err := decodeRequest(r, data, PermissionVoteProposal)
	_ = datastore
	_ = user
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}

	hexTx, err := createSignedVoteProposalMsg(keyManager, data)
	if err != nil {
		render.Render(w, r, ErrInvalidRequest(err))
		return
	}
	if data.BroadcastHost != "" {
		br, err := broadcastMessage(keyManager, data.BroadcastHost, data.BroadcastNetwork, hexTx)
		if err != nil {
			render.Render(w, r, ErrInvalidRequest(err))
			return
		}
		WriteJSONResponse(w, r, br)
	} else {
		WriteResponse(w, r, string(hexTx))
	}
}
