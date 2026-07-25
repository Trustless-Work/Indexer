package processors

import (
	"testing"

	"github.com/Trustless-Work/Indexer/internal/indexer/registry"
	"github.com/stellar/go-stellar-sdk/strkey"
	"github.com/stellar/go-stellar-sdk/xdr"
)

// contractIDBytes builds a deterministic ContractId from a seed byte.
func contractIDBytes(seed byte) xdr.ContractId {
	var id xdr.ContractId
	for i := range id {
		id[i] = seed
	}
	return id
}

func contractStrkey(t *testing.T, id xdr.ContractId) string {
	t.Helper()
	s, err := strkey.Encode(strkey.VersionByteContract, id[:])
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func symVal(s string) xdr.ScVal {
	sym := xdr.ScSymbol(s)
	return xdr.ScVal{Type: xdr.ScValTypeScvSymbol, Sym: &sym}
}

func contractAddrVal(id xdr.ContractId) xdr.ScVal {
	addr := xdr.ScAddress{Type: xdr.ScAddressTypeScAddressTypeContract, ContractId: &id}
	return xdr.ScVal{Type: xdr.ScValTypeScvAddress, Address: &addr}
}

func eventWith(emitter xdr.ContractId, topics ...xdr.ScVal) xdr.ContractEvent {
	return xdr.ContractEvent{
		ContractId: &emitter,
		Type:       xdr.ContractEventTypeContract,
		Body: xdr.ContractEventBody{
			V:  0,
			V0: &xdr.ContractEventV0{Topics: topics},
		},
	}
}

func eventsRegistry(t *testing.T, escrows ...string) *registry.Registry {
	t.Helper()
	reg, err := registry.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	reg.Seed(escrows)
	return reg
}

func TestClassify_DepositAttributionMatchesTransferTo(t *testing.T) {
	// TW-03 origin-side invariant: the envelope's escrow id IS the raw
	// event's `to` (topics[2]) — the consumer re-checks the same thing
	// against the XDR and DLQs a mismatch.
	token := contractIDBytes(0x01)
	from := contractIDBytes(0x02)
	escrow := contractIDBytes(0x03)
	escrowID := contractStrkey(t, escrow)

	d := NewEscrowEventDetector(eventsRegistry(t, escrowID))
	ev := eventWith(token, symVal("transfer"), contractAddrVal(from), contractAddrVal(escrow))

	fact, ok := d.classify(ev)
	if !ok {
		t.Fatal("a transfer into a tracked escrow must classify as a deposit")
	}
	if fact.Type != EscrowEventTypeDeposit || fact.EscrowID != escrowID {
		t.Fatalf("fact = %+v, want deposit attributed to %s", fact, escrowID)
	}
	if to, _ := transferToAddress(ev); to != fact.EscrowID {
		t.Fatalf("attribution invariant broken: envelope says %s, raw event says %s", fact.EscrowID, to)
	}
}

func TestClassify_FabricatedTransferWithoutFromAddressIsDropped(t *testing.T) {
	// Any contract can emit an event whose first topic spells
	// "transfer". A real SEP-41/SAC transfer carries an Address in
	// topics[1]; one that does not is not a transfer and must not
	// become a deposit row downstream.
	scam := contractIDBytes(0x0A)
	escrow := contractIDBytes(0x03)
	escrowID := contractStrkey(t, escrow)

	d := NewEscrowEventDetector(eventsRegistry(t, escrowID))
	ev := eventWith(scam, symVal("transfer"), symVal("junk"), contractAddrVal(escrow))

	if fact, ok := d.classify(ev); ok {
		t.Fatalf("transfer-shaped event without a from Address was forwarded: %+v", fact)
	}
}

func TestClassify_TransferToStrangerIsIgnored(t *testing.T) {
	token := contractIDBytes(0x01)
	from := contractIDBytes(0x02)
	stranger := contractIDBytes(0x0F)

	d := NewEscrowEventDetector(eventsRegistry(t, contractStrkey(t, contractIDBytes(0x03))))
	ev := eventWith(token, symVal("transfer"), contractAddrVal(from), contractAddrVal(stranger))

	if fact, ok := d.classify(ev); ok {
		t.Fatalf("transfer to a stranger was forwarded: %+v", fact)
	}
}

func TestClassify_EscrowEmittedEventForwardsAnyKind(t *testing.T) {
	escrow := contractIDBytes(0x03)
	escrowID := contractStrkey(t, escrow)

	d := NewEscrowEventDetector(eventsRegistry(t, escrowID))
	ev := eventWith(escrow, symVal("brand_new_kind"))

	fact, ok := d.classify(ev)
	if !ok || fact.Type != EscrowEventTypeEvent || fact.EscrowID != escrowID || fact.EventKind != "brand_new_kind" {
		t.Fatalf("fact = %+v ok=%v, want escrow event with open-ended kind", fact, ok)
	}
}
