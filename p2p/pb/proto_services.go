package pb

import (
	"math/big"

	"github.com/pkg/errors"
	"google.golang.org/protobuf/proto"

	"github.com/dominant-strategies/go-quai/common"
	"github.com/dominant-strategies/go-quai/core/types"
	"github.com/dominant-strategies/go-quai/log"
)

var EmptyResponse = errors.New("received empty reponse from peer")

func DecodeQuaiMessage(data []byte) (*QuaiMessage, error) {
	msg := &QuaiMessage{} // Assuming QuaiMessage is the struct generated by protoc
	if err := proto.Unmarshal(data, msg); err != nil {
		return nil, err // Return nil and the error if unmarshalling fails
	}
	return msg, nil // Return the decoded message and nil error if successful
}

// EncodeRequestMessage creates a marshaled protobuf message for a Quai Request.
// Returns the serialized protobuf message.
func EncodeQuaiRequest(id uint32, location common.Location, reqData interface{}, respDataType interface{}) ([]byte, error) {
	reqMsg := QuaiRequestMessage{
		Id:       id,
		Location: location.ProtoEncode(),
	}

	switch d := reqData.(type) {
	case common.Hash:
		reqMsg.Data = &QuaiRequestMessage_Hash{Hash: d.ProtoEncode()}
	case *big.Int:
		reqMsg.Data = &QuaiRequestMessage_Number{Number: d.Bytes()}
	default:
		return nil, errors.Errorf("unsupported request input data field type: %T", reqData)
	}

	switch respDataType.(type) {
	case *types.WorkObjectBlockView:
		reqMsg.Request = &QuaiRequestMessage_WorkObjectBlock{}
	case *types.WorkObjectHeaderView:
		reqMsg.Request = &QuaiRequestMessage_WorkObjectHeader{}
	case common.Hash:
		reqMsg.Request = &QuaiRequestMessage_BlockHash{}
	default:
		return nil, errors.Errorf("unsupported request data type: %T", respDataType)
	}

	quaiMsg := QuaiMessage{
		Payload: &QuaiMessage_Request{Request: &reqMsg},
	}
	return proto.Marshal(&quaiMsg)
}

// DecodeRequestMessage unmarshals a protobuf message into a Quai Request.
// Returns:
//  1. The request ID
//  2. The decoded type (i.e. *types.Header, *types.Block, etc)
//  3. The location
//  4. The request data
//  5. An error
func DecodeQuaiRequest(reqMsg *QuaiRequestMessage) (uint32, interface{}, common.Location, interface{}, error) {
	location := &common.Location{}
	location.ProtoDecode(reqMsg.Location)

	// First Decode the request data field
	var reqData interface{}
	switch d := reqMsg.Data.(type) {
	case *QuaiRequestMessage_Hash:
		hash := &common.Hash{}
		hash.ProtoDecode(d.Hash)
		reqData = hash
	case *QuaiRequestMessage_Number:
		reqData = new(big.Int).SetBytes(d.Number)
	}

	// Decode the request type
	var reqType interface{}
	switch reqMsg.Request.(type) {
	case *QuaiRequestMessage_WorkObjectBlock:
		reqType = &types.WorkObjectBlockView{}
	case *QuaiRequestMessage_WorkObjectHeader:
		reqType = &types.WorkObjectHeaderView{}
	case *QuaiRequestMessage_BlockHash:
		reqType = &common.Hash{}
	default:
		return reqMsg.Id, nil, common.Location{}, common.Hash{}, errors.Errorf("unsupported request type: %T", reqMsg.Request)
	}

	return reqMsg.Id, reqType, *location, reqData, nil
}

// EncodeResponse creates a marshaled protobuf message for a Quai Response.
// Returns the serialized protobuf message.
func EncodeQuaiResponse(id uint32, location common.Location, respDataType interface{}, data interface{}) ([]byte, error) {

	respMsg := QuaiResponseMessage{
		Id:       id,
		Location: location.ProtoEncode(),
	}

	var err error
	switch respDataType.(type) {
	case *types.WorkObjectBlockView:
		if data == nil {
			respMsg.Response = &QuaiResponseMessage_WorkObjectBlockView{}
		} else {
			protoWorkObjectBlock, err := data.(*types.WorkObjectBlockView).ProtoEncode()
			if err != nil {
				return nil, err
			}
			respMsg.Response = &QuaiResponseMessage_WorkObjectBlockView{WorkObjectBlockView: protoWorkObjectBlock}
		}

	case *types.WorkObjectHeaderView:
		protoWorkObjectHeader := &types.ProtoWorkObjectHeaderView{}
		if data == nil {
			respMsg.Response = &QuaiResponseMessage_WorkObjectHeaderView{}
		} else {
			protoWorkObjectHeader, err = data.(*types.WorkObjectHeaderView).ProtoEncode()
			if err != nil {
				return nil, err
			}
			respMsg.Response = &QuaiResponseMessage_WorkObjectHeaderView{WorkObjectHeaderView: protoWorkObjectHeader}
		}

	case *common.Hash:
		if data == nil {
			respMsg.Response = &QuaiResponseMessage_BlockHash{}
		} else {
			respMsg.Response = &QuaiResponseMessage_BlockHash{BlockHash: data.(common.Hash).ProtoEncode()}
		}

	default:
		return nil, errors.Errorf("unsupported response data type: %T", data)
	}

	quaiMsg := QuaiMessage{
		Payload: &QuaiMessage_Response{Response: &respMsg},
	}

	return proto.Marshal(&quaiMsg)
}

// Unmarshals a serialized protobuf message into a Quai Response message.
// Returns:
//  1. The request ID
//  2. The decoded type (i.e. *types.Header, *types.Block, etc)
//  3. An error
func DecodeQuaiResponse(respMsg *QuaiResponseMessage) (uint32, interface{}, error) {
	id := respMsg.Id
	sourceLocation := &common.Location{}
	sourceLocation.ProtoDecode(respMsg.Location)

	switch respMsg.Response.(type) {
	case *QuaiResponseMessage_WorkObjectHeaderView:
		protoWorkObject := respMsg.GetWorkObjectHeaderView()
		if protoWorkObject == nil {
			return id, nil, errors.New("nil response, and is not valid")
		}
		if protoWorkObject.WorkObject == nil {
			return id, nil, EmptyResponse
		}
		block := &types.WorkObjectHeaderView{
			WorkObject: &types.WorkObject{},
		}
		err := block.ProtoDecode(protoWorkObject, *sourceLocation)
		if err != nil {
			return id, nil, err
		}
		if messageMetrics != nil {
			messageMetrics.WithLabelValues("headers").Inc()
		}
		return id, block, nil
	case *QuaiResponseMessage_WorkObjectBlockView:
		protoWorkObject := respMsg.GetWorkObjectBlockView()
		if protoWorkObject == nil {
			return id, nil, errors.New("nil response, and is not valid")
		}
		if protoWorkObject.WorkObject == nil {
			return id, nil, EmptyResponse
		}
		block := &types.WorkObjectBlockView{
			WorkObject: &types.WorkObject{},
		}
		err := block.ProtoDecode(protoWorkObject, *sourceLocation)
		if err != nil {
			return id, nil, err
		}
		if messageMetrics != nil {
			messageMetrics.WithLabelValues("blocks").Inc()
		}
		return id, block, nil
	case *QuaiResponseMessage_BlockHash:
		blockHash := respMsg.GetBlockHash()
		if blockHash == nil {
			return id, nil, EmptyResponse
		}
		hash := common.Hash{}
		hash.ProtoDecode(blockHash)
		return id, hash, nil
	default:
		return id, nil, errors.Errorf("unsupported response type: %T", respMsg.Response)
	}
}

// Converts a custom go type to a proto type and marhsals it into a protobuf message
func ConvertAndMarshal(data interface{}) ([]byte, error) {
	switch data := data.(type) {
	case *types.WorkObjectHeaderView, *types.WorkObjectBlockView:
		switch data := data.(type) {
		case *types.WorkObjectHeaderView:
			protoBlock, err := data.ProtoEncode()
			if err != nil {
				return nil, err
			}
			return proto.Marshal(protoBlock)
		case *types.WorkObjectBlockView:
			protoBlock, err := data.ProtoEncode()
			if err != nil {
				return nil, err
			}
			return proto.Marshal(protoBlock)
		default:
			return nil, errors.New("unsupported data type")
		}
	case *types.Transaction:
		protoTransaction, err := data.ProtoEncode()
		if err != nil {
			return nil, err
		}
		return proto.Marshal(protoTransaction)
	case common.Hash:
		protoHash := data.ProtoEncode()
		return proto.Marshal(protoHash)
	case *types.Transactions:
		protoTransactions, err := data.ProtoEncode()
		if err != nil {
			return nil, err
		}
		return proto.Marshal(protoTransactions)
	case *types.WorkObjectHeader:
		log.Global.Tracef("marshalling block header: %+v", data)
		protoWoHeader, err := data.ProtoEncode()
		if err != nil {
			return nil, err
		}
		return proto.Marshal(protoWoHeader)
	default:
		return nil, errors.New("unsupported data type")
	}
}

// Unmarshals a protobuf message into a proto type and converts it to a custom go type
func UnmarshalAndConvert(data []byte, sourceLocation common.Location, dataPtr *interface{}, datatype interface{}) error {
	switch datatype.(type) {
	case *types.WorkObjectBlockView:
		protoWorkObject := &types.ProtoWorkObjectBlockView{}
		err := proto.Unmarshal(data, protoWorkObject)
		if err != nil {
			return err
		}

		workObjectBlockView := types.WorkObjectBlockView{}
		workObjectBlockView.WorkObject = &types.WorkObject{}
		err = workObjectBlockView.ProtoDecode(protoWorkObject, sourceLocation)
		if err != nil {
			return err
		}
		*dataPtr = workObjectBlockView
		return nil
	case *types.WorkObjectHeaderView:
		protoWorkObject := &types.ProtoWorkObjectHeaderView{}
		err := proto.Unmarshal(data, protoWorkObject)
		if err != nil {
			return err
		}
		workObjectHeaderView := types.WorkObjectHeaderView{}
		workObjectHeaderView.WorkObject = &types.WorkObject{}
		err = workObjectHeaderView.ProtoDecode(protoWorkObject, sourceLocation)
		if err != nil {
			return err
		}
		*dataPtr = workObjectHeaderView
		return nil
	case *types.WorkObjectHeader:
		protoWorkObjectHeader := &types.ProtoWorkObjectHeader{}
		err := proto.Unmarshal(data, protoWorkObjectHeader)
		if err != nil {
			return err
		}
		workObjectHeader := types.WorkObjectHeader{}
		err = workObjectHeader.ProtoDecode(protoWorkObjectHeader)
		if err != nil {
			return err
		}
		*dataPtr = workObjectHeader
		return nil
	case *types.Header:
		protoHeader := &types.ProtoHeader{}
		err := proto.Unmarshal(data, protoHeader)
		if err != nil {
			return err
		}
		header := types.Header{}
		err = header.ProtoDecode(protoHeader, sourceLocation)
		if err != nil {
			return err
		}
		*dataPtr = header
		return nil
	case *types.Transactions:
		protoTransactions := &types.ProtoTransactions{}
		err := proto.Unmarshal(data, protoTransactions)
		if err != nil {
			return err
		}
		transactions := types.Transactions{}
		err = transactions.ProtoDecode(protoTransactions, sourceLocation)
		if err != nil {
			return err
		}
		*dataPtr = transactions
		return nil
	case common.Hash:
		protoHash := &common.ProtoHash{}
		err := proto.Unmarshal(data, protoHash)
		if err != nil {
			return err
		}
		hash := common.Hash{}
		hash.ProtoDecode(protoHash)
		*dataPtr = hash
		return nil
	default:
		return errors.New("unsupported data type")
	}
}
