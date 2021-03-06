/*
Copyright 2019 The Vitess Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package grpcvtgateconn provides gRPC connectivity for VTGate.
package grpcvtgateconn

import (
	"flag"
	"io"

	"google.golang.org/grpc"

	"golang.org/x/net/context"
	"vitess.io/vitess/go/sqltypes"
	"vitess.io/vitess/go/vt/callerid"
	"vitess.io/vitess/go/vt/grpcclient"
	"vitess.io/vitess/go/vt/vterrors"
	"vitess.io/vitess/go/vt/vtgate/vtgateconn"

	binlogdatapb "vitess.io/vitess/go/vt/proto/binlogdata"
	querypb "vitess.io/vitess/go/vt/proto/query"
	topodatapb "vitess.io/vitess/go/vt/proto/topodata"
	vtgatepb "vitess.io/vitess/go/vt/proto/vtgate"
	vtgateservicepb "vitess.io/vitess/go/vt/proto/vtgateservice"
)

var (
	cert = flag.String("vtgate_grpc_cert", "", "the cert to use to connect")
	key  = flag.String("vtgate_grpc_key", "", "the key to use to connect")
	ca   = flag.String("vtgate_grpc_ca", "", "the server ca to use to validate servers when connecting")
	name = flag.String("vtgate_grpc_server_name", "", "the server name to use to validate server certificate")
)

func init() {
	vtgateconn.RegisterDialer("grpc", dial)
}

type vtgateConn struct {
	cc *grpc.ClientConn
	c  vtgateservicepb.VitessClient
}

func dial(ctx context.Context, addr string) (vtgateconn.Impl, error) {
	return DialWithOpts(ctx)(ctx, addr)
}

// DialWithOpts allows for custom dial options to be set on a vtgateConn.
func DialWithOpts(ctx context.Context, opts ...grpc.DialOption) vtgateconn.DialerFunc {
	return func(ctx context.Context, address string) (vtgateconn.Impl, error) {
		opt, err := grpcclient.SecureDialOption(*cert, *key, *ca, *name)
		if err != nil {
			return nil, err
		}

		opts = append(opts, opt)

		cc, err := grpcclient.Dial(address, grpcclient.FailFast(false), opts...)
		if err != nil {
			return nil, err
		}

		c := vtgateservicepb.NewVitessClient(cc)
		return &vtgateConn{
			cc: cc,
			c:  c,
		}, nil
	}
}

func (conn *vtgateConn) Execute(ctx context.Context, session *vtgatepb.Session, query string, bindVars map[string]*querypb.BindVariable) (*vtgatepb.Session, *sqltypes.Result, error) {
	request := &vtgatepb.ExecuteRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Session:  session,
		Query: &querypb.BoundQuery{
			Sql:           query,
			BindVariables: bindVars,
		},
	}
	response, err := conn.c.Execute(ctx, request)
	if err != nil {
		return session, nil, vterrors.FromGRPC(err)
	}
	if response.Error != nil {
		return response.Session, nil, vterrors.FromVTRPC(response.Error)
	}
	return response.Session, sqltypes.Proto3ToResult(response.Result), nil
}

func (conn *vtgateConn) ExecuteBatch(ctx context.Context, session *vtgatepb.Session, queryList []string, bindVarsList []map[string]*querypb.BindVariable) (*vtgatepb.Session, []sqltypes.QueryResponse, error) {
	queries := make([]*querypb.BoundQuery, len(queryList))
	for i, query := range queryList {
		bq := &querypb.BoundQuery{Sql: query}
		if len(bindVarsList) != 0 {
			bq.BindVariables = bindVarsList[i]
		}
		queries[i] = bq
	}
	request := &vtgatepb.ExecuteBatchRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Session:  session,
		Queries:  queries,
	}
	response, err := conn.c.ExecuteBatch(ctx, request)
	if err != nil {
		return session, nil, vterrors.FromGRPC(err)
	}
	if response.Error != nil {
		return response.Session, nil, vterrors.FromVTRPC(response.Error)
	}
	return response.Session, sqltypes.Proto3ToQueryReponses(response.Results), nil
}

func (conn *vtgateConn) StreamExecute(ctx context.Context, session *vtgatepb.Session, query string, bindVars map[string]*querypb.BindVariable) (sqltypes.ResultStream, error) {
	req := &vtgatepb.StreamExecuteRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Query: &querypb.BoundQuery{
			Sql:           query,
			BindVariables: bindVars,
		},
		Session: session,
	}
	stream, err := conn.c.StreamExecute(ctx, req)
	if err != nil {
		return nil, vterrors.FromGRPC(err)
	}
	return &streamExecuteAdapter{
		recv: func() (*querypb.QueryResult, error) {
			ser, err := stream.Recv()
			if err != nil {
				return nil, err
			}
			return ser.Result, nil
		},
	}, nil
}

func (conn *vtgateConn) ExecuteShards(ctx context.Context, query string, keyspace string, shards []string, bindVars map[string]*querypb.BindVariable, tabletType topodatapb.TabletType, session *vtgatepb.Session, options *querypb.ExecuteOptions) (*vtgatepb.Session, *sqltypes.Result, error) {
	request := &vtgatepb.ExecuteShardsRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Query: &querypb.BoundQuery{
			Sql:           query,
			BindVariables: bindVars,
		},
		Session:    session,
		Keyspace:   keyspace,
		Shards:     shards,
		TabletType: tabletType,
		Options:    options,
	}
	response, err := conn.c.ExecuteShards(ctx, request)
	if err != nil {
		return session, nil, vterrors.FromGRPC(err)
	}
	if response.Error != nil {
		return response.Session, nil, vterrors.FromVTRPC(response.Error)
	}
	return response.Session, sqltypes.Proto3ToResult(response.Result), nil
}

func (conn *vtgateConn) ExecuteKeyspaceIds(ctx context.Context, query string, keyspace string, keyspaceIds [][]byte, bindVars map[string]*querypb.BindVariable, tabletType topodatapb.TabletType, session *vtgatepb.Session, options *querypb.ExecuteOptions) (*vtgatepb.Session, *sqltypes.Result, error) {
	request := &vtgatepb.ExecuteKeyspaceIdsRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Query: &querypb.BoundQuery{
			Sql:           query,
			BindVariables: bindVars,
		},
		Session:     session,
		Keyspace:    keyspace,
		KeyspaceIds: keyspaceIds,
		TabletType:  tabletType,
		Options:     options,
	}
	response, err := conn.c.ExecuteKeyspaceIds(ctx, request)
	if err != nil {
		return session, nil, vterrors.FromGRPC(err)
	}
	if response.Error != nil {
		return response.Session, nil, vterrors.FromVTRPC(response.Error)
	}
	return response.Session, sqltypes.Proto3ToResult(response.Result), nil
}

func (conn *vtgateConn) ExecuteKeyRanges(ctx context.Context, query string, keyspace string, keyRanges []*topodatapb.KeyRange, bindVars map[string]*querypb.BindVariable, tabletType topodatapb.TabletType, session *vtgatepb.Session, options *querypb.ExecuteOptions) (*vtgatepb.Session, *sqltypes.Result, error) {
	request := &vtgatepb.ExecuteKeyRangesRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Query: &querypb.BoundQuery{
			Sql:           query,
			BindVariables: bindVars,
		},
		Session:    session,
		Keyspace:   keyspace,
		KeyRanges:  keyRanges,
		TabletType: tabletType,
		Options:    options,
	}
	response, err := conn.c.ExecuteKeyRanges(ctx, request)
	if err != nil {
		return session, nil, vterrors.FromGRPC(err)
	}
	if response.Error != nil {
		return response.Session, nil, vterrors.FromVTRPC(response.Error)
	}
	return response.Session, sqltypes.Proto3ToResult(response.Result), nil
}

func (conn *vtgateConn) ExecuteEntityIds(ctx context.Context, query string, keyspace string, entityColumnName string, entityKeyspaceIDs []*vtgatepb.ExecuteEntityIdsRequest_EntityId, bindVars map[string]*querypb.BindVariable, tabletType topodatapb.TabletType, session *vtgatepb.Session, options *querypb.ExecuteOptions) (*vtgatepb.Session, *sqltypes.Result, error) {
	request := &vtgatepb.ExecuteEntityIdsRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Query: &querypb.BoundQuery{
			Sql:           query,
			BindVariables: bindVars,
		},
		Session:           session,
		Keyspace:          keyspace,
		EntityColumnName:  entityColumnName,
		EntityKeyspaceIds: entityKeyspaceIDs,
		TabletType:        tabletType,
		Options:           options,
	}
	response, err := conn.c.ExecuteEntityIds(ctx, request)
	if err != nil {
		return session, nil, vterrors.FromGRPC(err)
	}
	if response.Error != nil {
		return response.Session, nil, vterrors.FromVTRPC(response.Error)
	}
	return response.Session, sqltypes.Proto3ToResult(response.Result), nil
}

func (conn *vtgateConn) ExecuteBatchShards(ctx context.Context, queries []*vtgatepb.BoundShardQuery, tabletType topodatapb.TabletType, asTransaction bool, session *vtgatepb.Session, options *querypb.ExecuteOptions) (*vtgatepb.Session, []sqltypes.Result, error) {
	request := &vtgatepb.ExecuteBatchShardsRequest{
		CallerId:      callerid.EffectiveCallerIDFromContext(ctx),
		Session:       session,
		Queries:       queries,
		TabletType:    tabletType,
		AsTransaction: asTransaction,
		Options:       options,
	}
	response, err := conn.c.ExecuteBatchShards(ctx, request)
	if err != nil {
		return session, nil, vterrors.FromGRPC(err)
	}
	if response.Error != nil {
		return response.Session, nil, vterrors.FromVTRPC(response.Error)
	}
	return response.Session, sqltypes.Proto3ToResults(response.Results), nil
}

func (conn *vtgateConn) ExecuteBatchKeyspaceIds(ctx context.Context, queries []*vtgatepb.BoundKeyspaceIdQuery, tabletType topodatapb.TabletType, asTransaction bool, session *vtgatepb.Session, options *querypb.ExecuteOptions) (*vtgatepb.Session, []sqltypes.Result, error) {
	var s *vtgatepb.Session
	if session != nil {
		s = session
	}
	request := &vtgatepb.ExecuteBatchKeyspaceIdsRequest{
		CallerId:      callerid.EffectiveCallerIDFromContext(ctx),
		Session:       s,
		Queries:       queries,
		TabletType:    tabletType,
		AsTransaction: asTransaction,
		Options:       options,
	}
	response, err := conn.c.ExecuteBatchKeyspaceIds(ctx, request)
	if err != nil {
		return session, nil, vterrors.FromGRPC(err)
	}
	if response.Error != nil {
		return response.Session, nil, vterrors.FromVTRPC(response.Error)
	}
	return response.Session, sqltypes.Proto3ToResults(response.Results), nil
}

type streamExecuteAdapter struct {
	recv   func() (*querypb.QueryResult, error)
	fields []*querypb.Field
}

func (a *streamExecuteAdapter) Recv() (*sqltypes.Result, error) {
	qr, err := a.recv()
	if err != nil {
		return nil, vterrors.FromGRPC(err)
	}
	if a.fields == nil {
		a.fields = qr.Fields
	}
	return sqltypes.CustomProto3ToResult(a.fields, qr), nil
}

func (conn *vtgateConn) StreamExecuteShards(ctx context.Context, query string, keyspace string, shards []string, bindVars map[string]*querypb.BindVariable, tabletType topodatapb.TabletType, options *querypb.ExecuteOptions) (sqltypes.ResultStream, error) {
	req := &vtgatepb.StreamExecuteShardsRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Query: &querypb.BoundQuery{
			Sql:           query,
			BindVariables: bindVars,
		},
		Keyspace:   keyspace,
		Shards:     shards,
		TabletType: tabletType,
		Options:    options,
	}
	stream, err := conn.c.StreamExecuteShards(ctx, req)
	if err != nil {
		return nil, vterrors.FromGRPC(err)
	}
	return &streamExecuteAdapter{
		recv: func() (*querypb.QueryResult, error) {
			ser, err := stream.Recv()
			if err != nil {
				return nil, err
			}
			return ser.Result, nil
		},
	}, nil
}

func (conn *vtgateConn) StreamExecuteKeyRanges(ctx context.Context, query string, keyspace string, keyRanges []*topodatapb.KeyRange, bindVars map[string]*querypb.BindVariable, tabletType topodatapb.TabletType, options *querypb.ExecuteOptions) (sqltypes.ResultStream, error) {
	req := &vtgatepb.StreamExecuteKeyRangesRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Query: &querypb.BoundQuery{
			Sql:           query,
			BindVariables: bindVars,
		},
		Keyspace:   keyspace,
		KeyRanges:  keyRanges,
		TabletType: tabletType,
		Options:    options,
	}
	stream, err := conn.c.StreamExecuteKeyRanges(ctx, req)
	if err != nil {
		return nil, vterrors.FromGRPC(err)
	}
	return &streamExecuteAdapter{
		recv: func() (*querypb.QueryResult, error) {
			ser, err := stream.Recv()
			if err != nil {
				return nil, err
			}
			return ser.Result, nil
		},
	}, nil
}

func (conn *vtgateConn) StreamExecuteKeyspaceIds(ctx context.Context, query string, keyspace string, keyspaceIds [][]byte, bindVars map[string]*querypb.BindVariable, tabletType topodatapb.TabletType, options *querypb.ExecuteOptions) (sqltypes.ResultStream, error) {
	req := &vtgatepb.StreamExecuteKeyspaceIdsRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Query: &querypb.BoundQuery{
			Sql:           query,
			BindVariables: bindVars,
		},
		Keyspace:    keyspace,
		KeyspaceIds: keyspaceIds,
		TabletType:  tabletType,
		Options:     options,
	}
	stream, err := conn.c.StreamExecuteKeyspaceIds(ctx, req)
	if err != nil {
		return nil, vterrors.FromGRPC(err)
	}
	return &streamExecuteAdapter{
		recv: func() (*querypb.QueryResult, error) {
			ser, err := stream.Recv()
			if err != nil {
				return nil, err
			}
			return ser.Result, nil
		},
	}, nil
}

func (conn *vtgateConn) Begin(ctx context.Context, singledb bool) (*vtgatepb.Session, error) {
	request := &vtgatepb.BeginRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		SingleDb: singledb,
	}
	response, err := conn.c.Begin(ctx, request)
	if err != nil {
		return nil, vterrors.FromGRPC(err)
	}
	return response.Session, nil
}

func (conn *vtgateConn) Commit(ctx context.Context, session *vtgatepb.Session, twopc bool) error {
	request := &vtgatepb.CommitRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Session:  session,
		Atomic:   twopc,
	}
	_, err := conn.c.Commit(ctx, request)
	return vterrors.FromGRPC(err)
}

func (conn *vtgateConn) Rollback(ctx context.Context, session *vtgatepb.Session) error {
	request := &vtgatepb.RollbackRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Session:  session,
	}
	_, err := conn.c.Rollback(ctx, request)
	return vterrors.FromGRPC(err)
}

func (conn *vtgateConn) ResolveTransaction(ctx context.Context, dtid string) error {
	request := &vtgatepb.ResolveTransactionRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Dtid:     dtid,
	}
	_, err := conn.c.ResolveTransaction(ctx, request)
	return vterrors.FromGRPC(err)
}

func (conn *vtgateConn) MessageStream(ctx context.Context, keyspace string, shard string, keyRange *topodatapb.KeyRange, name string, callback func(*sqltypes.Result) error) error {
	request := &vtgatepb.MessageStreamRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Keyspace: keyspace,
		Shard:    shard,
		KeyRange: keyRange,
		Name:     name,
	}
	stream, err := conn.c.MessageStream(ctx, request)
	if err != nil {
		return vterrors.FromGRPC(err)
	}
	var fields []*querypb.Field
	for {
		r, err := stream.Recv()
		if err != nil {
			if err != io.EOF {
				return vterrors.FromGRPC(err)
			}
			return nil
		}
		if fields == nil {
			fields = r.Result.Fields
		}
		err = callback(sqltypes.CustomProto3ToResult(fields, r.Result))
		if err != nil {
			return err
		}
	}
}

func (conn *vtgateConn) MessageAck(ctx context.Context, keyspace string, name string, ids []*querypb.Value) (int64, error) {
	request := &vtgatepb.MessageAckRequest{
		CallerId: callerid.EffectiveCallerIDFromContext(ctx),
		Keyspace: keyspace,
		Name:     name,
		Ids:      ids,
	}
	r, err := conn.c.MessageAck(ctx, request)
	if err != nil {
		return 0, vterrors.FromGRPC(err)
	}
	return int64(r.Result.RowsAffected), nil
}

func (conn *vtgateConn) MessageAckKeyspaceIds(ctx context.Context, keyspace string, name string, idKeyspaceIDs []*vtgatepb.IdKeyspaceId) (int64, error) {
	request := &vtgatepb.MessageAckKeyspaceIdsRequest{
		CallerId:      callerid.EffectiveCallerIDFromContext(ctx),
		Keyspace:      keyspace,
		Name:          name,
		IdKeyspaceIds: idKeyspaceIDs,
	}
	r, err := conn.c.MessageAckKeyspaceIds(ctx, request)
	if err != nil {
		return 0, vterrors.FromGRPC(err)
	}
	return int64(r.Result.RowsAffected), nil
}

func (conn *vtgateConn) GetSrvKeyspace(ctx context.Context, keyspace string) (*topodatapb.SrvKeyspace, error) {
	request := &vtgatepb.GetSrvKeyspaceRequest{
		Keyspace: keyspace,
	}
	response, err := conn.c.GetSrvKeyspace(ctx, request)
	if err != nil {
		return nil, vterrors.FromGRPC(err)
	}
	return response.SrvKeyspace, nil
}

type vstreamAdapter struct {
	stream vtgateservicepb.Vitess_VStreamClient
}

func (a *vstreamAdapter) Recv() ([]*binlogdatapb.VEvent, error) {
	r, err := a.stream.Recv()
	if err != nil {
		return nil, vterrors.FromGRPC(err)
	}
	return r.Events, nil
}

func (conn *vtgateConn) VStream(ctx context.Context, tabletType topodatapb.TabletType, vgtid *binlogdatapb.VGtid, filter *binlogdatapb.Filter) (vtgateconn.VStreamReader, error) {
	req := &vtgatepb.VStreamRequest{
		CallerId:   callerid.EffectiveCallerIDFromContext(ctx),
		TabletType: tabletType,
		Vgtid:      vgtid,
		Filter:     filter,
	}
	stream, err := conn.c.VStream(ctx, req)
	if err != nil {
		return nil, vterrors.FromGRPC(err)
	}
	return &vstreamAdapter{
		stream: stream,
	}, nil
}

func (conn *vtgateConn) Close() {
	conn.cc.Close()
}
