package main

// Online mode: one live competitive match against the server.
//
// The engine simulates every participant as a human actor. The local player
// is driven by the client's key actions; everyone else follows the scene
// facts that arrive on the QQTPPP UDP channel (PLAYER_MOVE paths, bubble
// placements, hit and death reports). The sidecar owns that UDP socket. TCP
// stays with the Python client, which forwards the in-match notifications it
// receives and sends the reliable REQUEST_PLAY (0x0083) mirrors returned in
// tcp_out.
//
// When the local player is the arbitrator (the room owner, which is always
// the case in a room filled with server AIs), the local engine decides every
// shared outcome and publishes it: explosions (0x0FA4), pickups (0x0FAD),
// kills (0x0FA9), rescues (0x0FAB), the bird's item supply (0x0FAE), pushed
// map elements (0x0FB3), kicked bubbles (0x139C) and avatar recoveries
// (0x10E0). It answers the other players' requests for them. The server's
// AIs depend on these notifications and on the reliable mirror of their own
// hit reports (0x0FA5).
//
// Otherwise the engine runs with native outcome authority and the local
// player as its request author: the arbitrator's notifications are the only
// shared outcomes, and the local player only asks (0x0FAC, 0x0FA8, 0x0FAA,
// 0x0FB2, 0x0FB4, sent to the arbitrator alone). Its own hits (0x0FA5) and
// trap death (0x0FA7) remain its own to report.
//
// See client/native/docs/match-protocol.md for the wire formats.

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strconv"
	"time"

	"qqtang/internal/clientdata/sceneelement"
	"qqtang/internal/game/battleengine"
	"qqtang/internal/game/mapdata"
	"qqtang/internal/protocol/game"
)

const (
	requestPlayCommand = 0x0083
	// Native clients repeat the room-peer rendezvous about every 3 s while
	// negotiating; the server's mode-0 lease is 15 s.
	rendezvousInterval = 3 * time.Second
	presenceInterval   = time.Second
	movementHeartbeat  = 150
	// Trapped players die when their bubble pops. The server uses 6 s for its
	// AIs; native clients time it themselves.
	defaultTrapMS = 6000
	curePropID    = 20020 // 万能药剂
	// Default bubble appearance (PLAYER_USE_BOMB.BombID) and its GameTime
	// copied from a native capture, as the server's AIs send them.
	defaultBombAppearance = 0xF20C7FFF
	bombGameTime          = 0xFFFF0065
	maxCatchUpTicks       = 50
	tickMS                = 20 // the live server's world tick
	// Server AIs are announced as UIN 1 900 000 000 + PlayerID.
	virtualUINBase = 1_900_000_000
	// An unanswered pickup request is repeated while the player stays on
	// the item, as the server's AIs do: at most 3 sends, 1 s apart.
	pickupRetryMS      = 1000
	pickupAttempts     = 3
	sceneRequestPeriod = 500 // ms between repeated push or kick requests
)

type onlineInit struct {
	Begin         string            `json:"begin"` // hex GameBegin 0x0089 payload
	LocalID       uint16            `json:"local_id"`
	LocalUIN      uint32            `json:"local_uin"`
	ServerHost    string            `json:"server_host"`
	ServerUDPPort int               `json:"server_udp_port"`
	UINs          map[string]uint32 `json:"uins"` // by player ID
	FreeRule      bool              `json:"free_rule"`
	TrapMS        uint32            `json:"trap_ms"`
	Autoplay      bool              `json:"autoplay"` // the test bot plays the local player
}

type tcpPacket struct {
	Command uint16 `json:"cmd"`
	Payload string `json:"payload"` // hex
}

type beginView struct {
	GameID     uint32   `json:"game_id"`
	MapID      uint32   `json:"map_id"`
	Rule       string   `json:"rule"`     // mapdata.CompetitiveRuleKind
	RoundMS    uint32   `json:"round_ms"` // round length including Ready/Go
	Arbitrator uint16   `json:"arbitrator"`
	Players    []player `json:"players"`
}

type netView struct {
	Presence   bool   `json:"presence"`
	PeersReady int    `json:"peers_ready"`
	Peers      int    `json:"peers"`
	Arbitrator uint16 `json:"arbitrator"`
	Received   int    `json:"received"`
}

// wireBomb is how peers name a bubble: owner, placement time and cell.
type wireBomb struct {
	owner    uint16
	placedAt uint32
	cell     battleengine.Cell
}

type movementProjection struct {
	initialized      bool
	sequence         uint16
	moving           bool
	direction        battleengine.Direction
	speed            byte
	path             battleengine.NativeMovementProjection
	lastSentAt       uint32
	lastSentPosition battleengine.Position
}

type onlineMatch struct {
	runtime    *battleengine.Runtime
	engine     *battleengine.Engine
	begin      game.GameBeginData
	local      uint16
	localUIN   uint32
	arbitrator uint16
	uins       map[uint16]uint32
	virtual    map[uint16]bool // server AIs
	trapMS     uint32
	started    time.Time
	log        io.Writer

	conn            *net.UDPConn
	inbound         chan []byte
	controlNumber   uint32
	multicastNumber uint32
	presence        bool
	lastPresence    time.Time
	lastRendezvous  time.Time
	peerReady       map[uint16]bool
	peerAnswered    map[uint16]bool // human peers whose rendezvous we answered
	lastNumber      map[uint16]uint32
	received        int

	// A bubble key press waits here for the next engine tick; the client
	// steps every frame but the engine only on 20 ms boundaries.
	bombPressed bool
	// Held-item uses requested over TCP and not yet confirmed, by action:
	// the scene clock of the request.
	useRequested map[uint8]uint32

	messageSeq uint32
	outbound   []outboundMessage
	tcpOut     []tcpPacket

	bombs     map[uint32]wireBomb // by engine bomb ID
	movement  movementProjection
	trappedAt uint32 // local player, engine clock; 0 when free
	// Explosion and pickup bookkeeping for arbitration.
	collected map[pickupKey]uint16 // by scene ID and cell: who collected it
	// Banana/glue field objects (actions 42/43) that already caught someone.
	fieldContacts map[fieldContactKey]bool
	// Item uses (0x0FB0) already applied, by player, time and item.
	itemUses map[[3]uint32]bool
	// The local player's room props (GAME_BEGIN NewItems) and the uses
	// requested over TCP (0x1174) but not yet confirmed (0x1175).
	prepared          []preparedView
	preparedRequested map[uint32]uint32

	// Non-arbitrator requests not answered yet: pickups by scene ID and cell,
	// pushes and kicks by what they move, with the scene clock of the last send.
	pickupRequests map[pickupKey]pickupRequest
	sceneRequests  map[sceneRequestKey]uint32
	mapElemSeq     uint16

	// Timed respawn (rule 5); nil for rule 1. eliminatedAt holds the engine
	// clock of each eliminated player's death.
	respawn      *respawnRule
	eliminatedAt map[uint16]uint32
	// Players who left the room mid-match; they never relive.
	departed map[uint16]bool

	bot *autoplayBot // --autoplay; nil when the keyboard plays
	// The carry rule (3 or 6), nil otherwise; bun and sculpture are the same
	// rule with its own type, for drawing.
	carry     carryRule
	bun       *bunRule
	sculpture *sculptureRule
	// The local player's last take or put request and when it was sent, so
	// standing on the spot does not flood the arbitrator.
	carryRequest     game.BunActionEvent
	carryRequestKind uint16
	carryRequestSent uint32
	// Rule 6's style-9 bubbles (held item 48) by engine bomb ID.
	styleNine map[uint32]bool
}

// respawnRule is a special rule's relive timing, from Client.exe's rule
// objects: slot 5 is the delay and rules 4 and 5 wait for the respawn cell
// (0x00615AAE).
type respawnRule struct {
	delayMS  uint32
	waitFree bool
}

type outboundMessage struct {
	data           game.BattleMessageData
	arbitratorOnly bool // requests go to the arbitrator alone
}

type pickupRequest struct {
	body     []byte
	sentAt   uint32
	attempts int
}

type sceneRequestKey struct {
	schema   uint16
	object   uint32
	from, to battleengine.Cell
}

type fieldContactKey struct {
	player uint16
	action uint8
	cell   battleengine.Cell
}

type pickupKey struct {
	scene uint32
	cell  battleengine.Cell
}

func newOnlineMatch(init command) (*onlineMatch, beginView, error) {
	options := init.Online
	payload, err := hex.DecodeString(options.Begin)
	if err != nil {
		return nil, beginView{}, fmt.Errorf("game begin: %w", err)
	}
	begin, err := game.ParseLengthPrefixedGameBeginDataNetwork(payload)
	if err != nil {
		return nil, beginView{}, fmt.Errorf("game begin: %w", err)
	}
	catalog, err := mapdata.LoadCatalog(init.ClientRoot)
	if err != nil {
		return nil, beginView{}, err
	}
	entry, ok := catalog.CompetitiveMap(begin.MapID)
	if !ok {
		return nil, beginView{}, fmt.Errorf("map %d is not a competitive map", begin.MapID)
	}
	view := beginView{GameID: begin.GameID, MapID: begin.MapID, Rule: string(entry.Rule), Arbitrator: begin.ArbitratorPlayerID}
	if spec, ok := mapdata.LookupCompetitiveRuleByNativeID(entry.NativeRule); ok {
		view.RoundMS = spec.RoundDurationMS
	}
	rules, respawn, err := ruleOptions(entry, options.LocalID, begin.ArbitratorPlayerID)
	if err != nil {
		return nil, beginView{}, err
	}
	participants := make([]battleengine.Participant, 0, len(begin.Players))
	uins := make(map[uint16]uint32, len(begin.Players))
	for _, info := range begin.Players {
		participant, err := battleengine.ParticipantFromNativeRole(info.PlayerID, uint16(info.RoleID), info.TeamID, battleengine.ParticipantHuman)
		if err != nil {
			return nil, beginView{}, err
		}
		participants = append(participants, participant)
		entry := player{ID: info.PlayerID, Role: uint16(info.RoleID), Team: info.TeamID}
		for _, item := range info.NewItems {
			entry.Prepared = append(entry.Prepared, preparedView{Item: item.ItemID, Count: item.Quantity})
		}
		view.Players = append(view.Players, entry)
		uin := options.UINs[strconv.Itoa(int(info.PlayerID))]
		if uin == 0 {
			uin = virtualUINBase + uint32(info.PlayerID)
		}
		uins[info.PlayerID] = uin
	}
	if _, present := uins[options.LocalID]; !present {
		return nil, beginView{}, fmt.Errorf("local player %d is not in the match", options.LocalID)
	}
	uins[options.LocalID] = options.LocalUIN
	virtual := map[uint16]bool{}
	for playerID, uin := range uins {
		if uin == virtualUINBase+uint32(playerID) {
			virtual[playerID] = true
		}
	}
	config, err := battleengine.ConfigFromCompetitiveMap(entry, battleengine.CompetitiveMapConfigOptions{
		SimulationSeed: uint64(begin.SpawnSeed)<<32 | uint64(begin.ItemSeed),
		SpawnSeed:      begin.SpawnSeed, ItemSeed: begin.ItemSeed,
		SpawnMode:            battleengine.NativeSpawnModeForMap(entry, options.FreeRule),
		Rules:                rules,
		Participants:         participants,
		RecordedWallItems:    wallItems(begin.NewItems),
		RecordedDelayedItems: wallItems(begin.Items),
		UseRecordedWallItems: true,
		AllowSpecialRules:    respawn != nil,
	})
	if err != nil {
		return nil, beginView{}, err
	}
	engine, err := battleengine.New(config)
	if err != nil {
		return nil, beginView{}, err
	}
	runtime, err := battleengine.NewRuntime(engine, nil)
	if err != nil {
		return nil, beginView{}, err
	}
	address, err := net.ResolveUDPAddr("udp4", net.JoinHostPort(options.ServerHost, strconv.Itoa(options.ServerUDPPort)))
	if err != nil {
		return nil, beginView{}, err
	}
	conn, err := net.DialUDP("udp4", nil, address)
	if err != nil {
		return nil, beginView{}, err
	}
	trapMS := options.TrapMS
	if trapMS == 0 {
		trapMS = defaultTrapMS
	}
	match := &onlineMatch{
		runtime: runtime, engine: engine, begin: begin,
		local: options.LocalID, localUIN: options.LocalUIN, arbitrator: begin.ArbitratorPlayerID,
		uins: uins, virtual: virtual, trapMS: trapMS, started: time.Now(), log: os.Stderr,
		conn: conn, inbound: make(chan []byte, 256),
		peerReady: map[uint16]bool{}, peerAnswered: map[uint16]bool{}, lastNumber: map[uint16]uint32{},
		bombs: map[uint32]wireBomb{}, collected: map[pickupKey]uint16{},
		fieldContacts: map[fieldContactKey]bool{}, itemUses: map[[3]uint32]bool{}, useRequested: map[uint8]uint32{},
		preparedRequested: map[uint32]uint32{},
		pickupRequests:    map[pickupKey]pickupRequest{}, sceneRequests: map[sceneRequestKey]uint32{},
		respawn: respawn, eliminatedAt: map[uint16]uint32{}, departed: map[uint16]bool{},
	}
	switch entry.Rule {
	case mapdata.CompetitiveRuleBun:
		match.bun = newBunRule(entry, begin)
		match.carry = match.bun
	case mapdata.CompetitiveRuleSculpture:
		match.sculpture = newSculptureRule(engine, entry, begin, config.LooseObjects)
		match.carry, match.styleNine = match.sculpture, map[uint32]bool{}
		// 0x0060A772: every player starts with one style-9 bubble.
		for _, info := range begin.Players {
			engine.GrantHeldAction(info.PlayerID, sculptureBombItem, 1)
			match.sculpture.bombLeft[info.PlayerID] = true
		}
	}
	if options.Autoplay {
		match.bot = newAutoplayBot(match.local)
		match.bot.bun, match.bot.sculpture = match.bun, match.sculpture
		match.logf("autoplay: the test bot plays player %d", match.local)
	}
	for _, entry := range view.Players {
		if entry.ID == match.local {
			match.prepared = append([]preparedView(nil), entry.Prepared...)
		}
	}
	go match.receive()
	match.logf("game %d map %d: local %d, arbitrator %d, %d players", begin.GameID, begin.MapID, match.local, match.arbitrator, len(begin.Players))
	return match, view, nil
}

// onlineRules: the arbitrator's engine decides everything except the other
// players' hits, which they report themselves. Any other client's engine
// only follows the arbitrator and asks on behalf of its own player.
func onlineRules(local, arbitrator uint16) battleengine.Rules {
	if local == arbitrator {
		return battleengine.Rules{TickMS: tickMS, LocalHitPlayerID: local}
	}
	return battleengine.Rules{TickMS: tickMS, NativeOutcomeAuthority: true, NativeRequestPlayerID: local}
}

// ruleOptions adds a special rule's engine options to onlineRules. Only
// treasure (rule 5) is implemented besides rule 1: gems 150..152 are carried
// objectives, a player who dies with gems drops only them, players relive
// 5 s after death once their spawn cell is free, and the server decides the
// result.
func ruleOptions(entry mapdata.CompetitiveMap, local, arbitrator uint16) (battleengine.Rules, *respawnRule, error) {
	rules := onlineRules(local, arbitrator)
	switch entry.Rule {
	case mapdata.CompetitiveRuleOrdinary:
		return rules, nil, nil
	case mapdata.CompetitiveRuleBun:
		// Rule 3's slots 4 and 5: relive after 10 s (Tutorial2.qbv agrees),
		// without waiting for the spawn cell (that is rules 4 and 5 only).
		rules.ExternalConclusion = true
		return rules, &respawnRule{delayMS: 10_000}, nil
	case mapdata.CompetitiveRuleWrestle:
		// Rule 4 (比武): ordinary combat scored by kills on the server.
		// Slots 4 and 5 (vtable 0x007A4464): relive after 5 s once the spawn
		// cell is free; slot 6 (0x0060B264): the death drop goes to the bird.
		// The charged skills (0x115C/0x115B) are left out: their key combos
		// are in protected code and their pose images (misc214..219) are not
		// in this client's files.
		rules.ExternalConclusion = true
		rules.DeathDropRecycle = true
		return rules, &respawnRule{delayMS: 5000, waitFree: true}, nil
	case mapdata.CompetitiveRuleSculpture:
		// Rule 6's slots 4 and 5 (vtable 0x7A43CC): relive after 5 s, not
		// waiting for the spawn cell. Fragments 161..165 are loose objects.
		rules.ExternalConclusion = true
		for index := range rules.LooseObjectSceneIDs {
			rules.LooseObjectSceneIDs[index] = uint32(sculptureFirstScene + index)
		}
		return rules, &respawnRule{delayMS: 5000}, nil
	case mapdata.CompetitiveRuleTreasure:
		rules.ObjectiveSceneIDs = [battleengine.MaxObjectiveKinds]uint32{
			uint32(sceneelement.TreasureGemOne), uint32(sceneelement.TreasureGemTwo), uint32(sceneelement.TreasureGemThree),
		}
		rules.ObjectiveDeathDrops = true
		rules.ObjectiveDepartureRecycle = true
		rules.ExternalConclusion = true
		return rules, &respawnRule{delayMS: 5000, waitFree: true}, nil
	default:
		return battleengine.Rules{}, nil, fmt.Errorf("map %d uses rule %s, which this client does not play yet", entry.ID, entry.Rule)
	}
}

func wallItems(items []game.GameItemType) []mapdata.CompetitiveWallItem {
	result := make([]mapdata.CompetitiveWallItem, 0, len(items))
	for _, item := range items {
		if item.ItemID != 0 && item.Quantity > 0 {
			result = append(result, mapdata.CompetitiveWallItem{SceneID: item.ItemID, Quantity: item.Quantity})
		}
	}
	return result
}

func (match *onlineMatch) logf(format string, args ...any) {
	fmt.Fprintf(match.log, "[sidecar] "+format+"\n", args...)
}

func (match *onlineMatch) close() {
	_ = match.conn.Close()
}

func (match *onlineMatch) receive() {
	buffer := make([]byte, 2048)
	for {
		size, err := match.conn.Read(buffer)
		if errors.Is(err, net.ErrClosed) {
			return
		}
		if err != nil {
			// A connected UDP socket reports ICMP port-unreachable as a read
			// error; the server may simply not be listening yet.
			continue
		}
		match.inbound <- append([]byte(nil), buffer[:size]...)
	}
}

// sceneClock is the scene time the engine should have reached: the 3 s
// Ready/Go countdown runs from GAME_BEGIN, then fixed 20 ms ticks.
func (match *onlineMatch) sceneClock() uint32 {
	elapsed := uint32(time.Since(match.started) / time.Millisecond)
	if elapsed < battleengine.NativeRoundStartClockMS {
		return battleengine.NativeRoundStartClockMS
	}
	return battleengine.NativeRoundStartClockMS + (elapsed-battleengine.NativeRoundStartClockMS)/tickMS*tickMS
}

func (match *onlineMatch) clientTag() uint32 {
	return uint32(time.Since(match.started) / time.Millisecond)
}

// step handles the TCP notifications forwarded by the client, drains the
// UDP socket, advances the engine to the wall clock and publishes the local
// facts. It returns the engine events of this call.
func (match *onlineMatch) step(step command) ([]battleengine.Event, error) {
	for _, packet := range step.TCP {
		match.handleTCP(packet)
	}
	match.negotiate()
	var events []battleengine.Event
	events = append(events, match.drain()...)
	match.noteEliminations(events)
	// A held item is not used directly: its use is a TCP request (0x0FAF)
	// and takes effect when the server's 0x0FB0 comes back (handleTCP).
	local := battleengine.Action{PlayerID: match.local}
	for _, entry := range step.Actions {
		if entry.ID == match.local {
			local.Move = battleengine.Direction(entry.Move)
			match.bombPressed = match.bombPressed || entry.Bomb
			if entry.Use != 0 {
				match.requestItemUse(entry.Use)
			}
			if entry.Prop != 0 {
				match.requestPreparedUse(entry.Prop)
			}
		}
	}
	target := match.sceneClock()
	if target <= battleengine.NativeRoundStartClockMS {
		match.bombPressed = false // Ready/Go: no control yet
	}
	for ticks := 0; ticks < maxCatchUpTicks && match.engine.ElapsedMS() < target && !match.engine.Terminal().Ended; ticks++ {
		before := match.runtime.EngineSnapshot()
		action := local
		action.PlaceBomb, match.bombPressed = match.bombPressed, false
		if match.bot != nil && match.localActive() {
			chosen := match.bot.act(match.engine)
			action.Move, action.PlaceBomb = chosen.Move, chosen.PlaceBomb
			if chosen.UseActionID != 0 {
				match.requestItemUse(chosen.UseActionID)
			}
		}
		var actions []battleengine.Action
		if action.PlayerID != 0 && match.localActive() {
			actions = []battleengine.Action{action}
		}
		tickEvents, err := match.runtime.Step(actions)
		if err != nil {
			return events, err
		}
		after := match.runtime.EngineSnapshot()
		match.publishMovement(before, after, action)
		tickEvents = append(tickEvents, match.expireLocalTrap()...)
		match.publishEvents(after, tickEvents)
		match.noteEliminations(tickEvents)
		tickEvents = append(tickEvents, match.respawnDue()...)
		match.carryTick()
		events = append(events, tickEvents...)
	}
	match.retryPickupRequests()
	match.flush()
	return events, nil
}

func (match *onlineMatch) localActive() bool {
	for _, actor := range match.engine.Actors() {
		if actor.PlayerID == match.local {
			return actor.State == battleengine.ActorActive
		}
	}
	return false
}

func (match *onlineMatch) netView() *netView {
	ready := 0
	for _, ok := range match.peerReady {
		if ok {
			ready++
		}
	}
	return &netView{Presence: match.presence, PeersReady: ready, Peers: len(match.uins) - 1, Arbitrator: match.arbitrator, Received: match.received}
}

func (match *onlineMatch) takeTCP() []tcpPacket {
	out := match.tcpOut
	match.tcpOut = nil
	return out
}

// UDP control ---------------------------------------------------------------

func (match *onlineMatch) localEndpoint() game.LegacyUDPEndpoint {
	var endpoint game.LegacyUDPEndpoint
	if address, ok := match.conn.LocalAddr().(*net.UDPAddr); ok {
		copy(endpoint.IPv4[:], address.IP.To4())
		endpoint.Port = uint16(address.Port)
	}
	return endpoint
}

func (match *onlineMatch) header(kind game.LegacyUDPMessageType, number uint32) game.LegacyUDPControlHeader {
	return game.LegacyUDPControlHeader{PacketNumber: number, PlayerID: match.local, UIN: match.localUIN, Type: kind}
}

func (match *onlineMatch) sendControl(packet game.LegacyUDPControlPacket) {
	data, err := packet.Encode()
	if err != nil {
		match.logf("encode UDP type %d: %v", packet.Header.Type, err)
		return
	}
	_, _ = match.conn.Write(data)
}

// negotiate registers the presence endpoint (Type 1) and repeats a room-peer
// rendezvous (Type 3) with every other participant until it answers. For
// server AIs the server answers the rendezvous itself; their policies stay
// frozen until every human has completed it.
func (match *onlineMatch) negotiate() {
	now := time.Now()
	if !match.presence && now.Sub(match.lastPresence) >= presenceInterval {
		match.lastPresence = now
		match.controlNumber++
		endpoint := match.localEndpoint()
		match.sendControl(game.LegacyUDPControlPacket{Header: match.header(game.LegacyUDPPresenceType, match.controlNumber), Presence: &endpoint})
		return
	}
	if !match.presence || now.Sub(match.lastRendezvous) < rendezvousInterval {
		return
	}
	match.lastRendezvous = now
	for _, playerID := range match.peerIDs() {
		if !match.peerReady[playerID] {
			match.sendRendezvous(playerID)
		}
	}
}

func (match *onlineMatch) sendRendezvous(playerID uint16) {
	match.controlNumber++
	match.sendControl(game.LegacyUDPControlPacket{
		Header:   match.header(game.LegacyUDPRoomPeerType, match.controlNumber),
		RoomPeer: &game.LegacyUDPRoomPeerPayload{TargetPlayerID: playerID, TargetUIN: match.uins[playerID], Endpoint: match.localEndpoint()},
	})
}

func (match *onlineMatch) peerIDs() []uint16 {
	ids := make([]uint16, 0, len(match.uins))
	for playerID := range match.uins {
		if playerID != match.local {
			ids = append(ids, playerID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

func (match *onlineMatch) drain() []battleengine.Event {
	var events []battleengine.Event
	for {
		select {
		case data := <-match.inbound:
			events = append(events, match.handleDatagram(data)...)
		default:
			return events
		}
	}
}

func (match *onlineMatch) handleDatagram(data []byte) []battleengine.Event {
	packet, err := game.DecodeLegacyUDPControlPacket(data)
	if err != nil {
		return nil
	}
	switch {
	case packet.Presence != nil:
		if !match.presence {
			match.logf("UDP presence registered")
		}
		match.presence = true
		match.lastRendezvous = time.Time{}
	case packet.RoomPeer != nil:
		peer := packet.Header.PlayerID
		if !match.peerReady[peer] {
			match.logf("UDP peer %d ready", peer)
		}
		match.peerReady[peer] = true
		// A human peer's rendezvous is answered once (match-protocol.md §11
		// step 3): the server forwards a Type-3 only to a registered target,
		// so a peer that registered after our first ones never saw any and
		// would keep asking. The server answers for its AIs itself.
		if !match.virtual[peer] && !match.peerAnswered[peer] {
			match.peerAnswered[peer] = true
			match.sendRendezvous(peer)
		}
	case packet.Multicast != nil:
		sender := packet.Header.PlayerID
		if last, seen := match.lastNumber[sender]; seen && int32(packet.Header.PacketNumber-last) <= 0 {
			return nil
		}
		match.lastNumber[sender] = packet.Header.PacketNumber
		batch, err := game.DecodeQQTPPPGameplayBatch(packet.Multicast.Data)
		if err != nil || batch.GameID != match.begin.GameID {
			return nil
		}
		var events []battleengine.Event
		for _, entry := range batch.Entries {
			if entry.Package.PlayerID != sender {
				continue
			}
			for _, message := range entry.Package.Messages {
				match.received++
				applied := match.applyRemote(sender, message)
				match.forgetPickups(applied)
				events = append(events, applied...)
			}
		}
		return events
	}
	return nil
}

// Remote scene facts --------------------------------------------------------

func (match *onlineMatch) applyRemote(sender uint16, message game.BattleMessageData) []battleengine.Event {
	if message.DataID > 0xffff {
		return nil
	}
	schema := uint16(message.DataID)
	event := game.GameEvent{Schema: schema, Body: message.Data}
	switch schema {
	case game.PlayerMoveSchema:
		movement, err := game.ParsePlayerMove(message.Data)
		if err != nil {
			return nil
		}
		for _, entry := range movement.Entries {
			if entry.PlayerID != sender {
				continue
			}
			move := entry.Move
			_ = match.runtime.ReconcileHumanMovementPath(sender, battleengine.NativeHumanMovement{
				Sequence: move.Sequence, TimeMS: move.TimeStamp,
				Current:   battleengine.Position{X: int32(move.CurrentPosX), Y: int32(move.CurrentPosY)},
				Corner:    battleengine.Position{X: int32(move.CornerPosX), Y: int32(move.CornerPosY)},
				End:       battleengine.Position{X: int32(move.EndPosX), Y: int32(move.EndPosY)},
				Direction: battleDirection(move.WalkAndDirection), Speed: move.Speed,
				Moving: move.WalkAndDirection&0x10 != 0, Forced: move.WalkAndDirection&0x20 != 0,
			})
		}
	case game.PlayerUseBomb, game.NotifyPlayerUseBomb:
		placed, err := game.ParsePlayerUseBombEvent(game.GameEvent{Schema: game.PlayerUseBomb, Body: message.Data})
		if err != nil || placed.PlayerID != sender {
			return nil
		}
		cell := battleengine.Cell{Row: int16(placed.Row), Col: int16(placed.Column)}
		placedAt := max(placed.ClientTime, battleengine.NativeRoundStartClockMS)
		accepted, err := match.runtime.AcceptHumanBombPlacement(sender, cell, placed.Power, placed.Property, placedAt)
		if err != nil {
			match.logf("bomb from %d rejected: %v", sender, err)
			return nil
		}
		match.bombs[accepted.BombID] = wireBomb{owner: sender, placedAt: placed.ClientTime, cell: cell}
		return []battleengine.Event{accepted}
	case game.PlayerBeExploded:
		hit, err := game.ParsePlayerExplodedEvent(event)
		if err != nil || hit.PlayerID != sender {
			return nil
		}
		accepted, changed, err := match.runtime.AcceptNativeActorHitFrom(hit.PlayerID, 0, battleengine.Position{X: int32(hit.PosX), Y: int32(hit.PosY)}, hit.IsAvatar)
		if err != nil {
			match.logf("hit of %d rejected: %v", hit.PlayerID, err)
		}
		if match.isArbitrator() && match.virtual[hit.PlayerID] {
			// The server commits a server AI's hit only from the arbitrator's
			// reliable copy of its exact report; humans mirror their own.
			match.mirror(schema, message.Data)
		}
		if changed {
			return []battleengine.Event{accepted}
		}
	case game.NotifyPlayerDieEvent:
		death, err := game.ParsePlayerDeathEvent(event)
		if err != nil || (death.PlayerID != sender && sender != match.arbitrator) {
			return nil
		}
		events, err := match.runtime.AcceptNativeElimination(death.PlayerID, 0, pickupsFromItems(death.Items))
		if err != nil {
			match.logf("death of %d rejected: %v", death.PlayerID, err)
		}
		if match.carry != nil {
			match.carry.onDeath(death.PlayerID, 0, false, battleengine.Position{X: int32(death.PosX), Y: int32(death.PosY)})
		}
		return events
	case game.RequestGetItem:
		if match.isArbitrator() {
			return match.arbitratePickup(event)
		}
	case game.RequestKillPlayer, game.RequestSavePlayer:
		if match.isArbitrator() {
			return match.arbitrateContact(event)
		}
	case game.NotifyPlayerKilled:
		killed, err := game.ParsePlayerKilledEvent(event)
		if err != nil || sender != match.arbitrator {
			return nil
		}
		events, _ := match.runtime.AcceptNativeElimination(killed.DestinationPlayerID, killed.PlayerID, pickupsFromItems(killed.Items))
		if match.carry != nil {
			match.carry.onDeath(killed.DestinationPlayerID, killed.PlayerID, true, battleengine.Position{X: int32(killed.PosX), Y: int32(killed.PosY)})
		}
		return events
	case game.NotifyPlayerSaved:
		saved, err := game.ParsePlayerInteractionEvent(event)
		if err != nil || sender != match.arbitrator {
			return nil
		}
		accepted, changed, _ := match.runtime.AcceptNativeRescue(saved.PlayerID, saved.DestinationPlayerID, battleengine.Position{X: int32(saved.PosX), Y: int32(saved.PosY)})
		if changed {
			return []battleengine.Event{accepted}
		}
	case game.NotifyPlayerGetItem:
		item, err := game.ParsePlayerItemEvent(event)
		if err != nil || sender != match.arbitrator || match.isArbitrator() {
			return nil
		}
		position := battleengine.Position{X: int32(item.PosX), Y: int32(item.PosY)}
		if item.PlayerID == 0xffff {
			// Refused: Client.exe answers with the request's item and position.
			delete(match.pickupRequests, pickupKey{scene: item.ItemID, cell: position.Cell()})
			return nil
		}
		if item.PlayerID == match.local {
			delete(match.pickupRequests, pickupKey{scene: item.ItemID, cell: position.Cell()})
		}
		if item.ItemID == 42 || item.ItemID == 43 {
			events, err := match.runtime.AcceptNativeFieldObjectContact(item.PlayerID, uint8(item.ItemID), position)
			if err != nil {
				match.logf("field object %d for %d rejected: %v", item.ItemID, item.PlayerID, err)
			}
			return events
		}
		accepted, err := match.runtime.AcceptNativePickup(item.PlayerID, item.ItemID, position)
		if err != nil {
			match.logf("pickup %d by %d rejected: %v", item.ItemID, item.PlayerID, err)
			return nil
		}
		delete(match.pickupRequests, pickupKey{scene: item.ItemID, cell: accepted.Cell})
		return []battleengine.Event{accepted}
	case game.NotifyDispatchItem:
		dispatch, err := game.ParseDispatchItemEvent(event)
		if err != nil || sender != match.arbitrator || match.isArbitrator() {
			return nil
		}
		pickups := make([]battleengine.Pickup, 0, len(dispatch.Items)+len(dispatch.DelayedItems))
		for _, item := range dispatch.Items {
			pickups = append(pickups, battleengine.Pickup{SceneID: item.ItemID, Cell: battleengine.Cell{Row: int16(item.Row), Col: int16(item.Col)}, State: battleengine.PickupAvailable})
		}
		for _, item := range dispatch.DelayedItems {
			pickups = append(pickups, battleengine.Pickup{SceneID: item.ItemID, Cell: battleengine.Cell{Row: int16(item.Row), Col: int16(item.Col)}, State: battleengine.PickupAvailable})
		}
		if err := match.runtime.AcceptNativePickupDispatch(dispatch.Time, pickups); err != nil {
			match.logf("item dispatch rejected: %v", err)
			return nil
		}
		forgotten := make([]battleengine.Event, 0, len(pickups))
		for _, pickup := range pickups {
			forgotten = append(forgotten, battleengine.Event{Kind: battleengine.EventPickupDispatched, SceneID: pickup.SceneID, Cell: pickup.Cell})
		}
		match.forgetPickups(forgotten)
	case game.NotifyItemExploded:
		destroyed, err := game.ParseItemsExplodedEvent(event)
		if err != nil || sender != match.arbitrator || match.isArbitrator() {
			return nil
		}
		if err := match.runtime.AcceptNativeItemDestruction(pickupsFromItems(destroyed.Items)); err != nil {
			match.logf("item destruction rejected: %v", err)
		}
	case game.NotifyRecoverAvatar:
		recovery, err := game.ParseAvatarRecoveryEvent(event)
		if err != nil || sender != match.arbitrator || match.isArbitrator() {
			return nil
		}
		accepted, changed, err := match.runtime.AcceptNativeTransformationRecovery(recovery.PlayerID, battleengine.Position{X: int32(recovery.PosX), Y: int32(recovery.PosY)})
		if err != nil {
			match.logf("avatar recovery of %d rejected: %v", recovery.PlayerID, err)
		}
		if changed {
			return []battleengine.Event{accepted}
		}
	case game.RequestMoveMapElement:
		if match.isArbitrator() {
			return match.arbitrateMapElementMove(sender, event)
		}
	case game.RequestMoveBomb:
		if match.isArbitrator() {
			return match.arbitrateBombMove(sender, event)
		}
	case game.NotifyMapElementMoved:
		moved, err := game.ParseMapElementMovedEvent(event)
		if err != nil || sender != match.arbitrator || match.isArbitrator() {
			return nil
		}
		source := battleengine.Cell{Row: int16(moved.Row), Col: int16(moved.Col)}
		accepted, err := match.runtime.AcceptNativeMapElementMovement(moved.PlayerID, moved.ElementID, source, battleDirection(moved.Direction))
		if err != nil {
			match.logf("map element %d move rejected: %v", moved.ElementID, err)
			return nil
		}
		delete(match.sceneRequests, sceneRequestKey{schema: game.RequestMoveMapElement, object: moved.ElementID, from: source, to: accepted.Cell})
		return []battleengine.Event{accepted}
	case game.NotifyPlayerMoveBomb:
		moved, err := game.ParseMoveBombEvent(event)
		if err != nil || sender != match.arbitrator || match.isArbitrator() {
			return nil
		}
		source := battleengine.Cell{Row: int16(moved.FromRow), Col: int16(moved.FromCol)}
		target := battleengine.Cell{Row: int16(moved.ToRow), Col: int16(moved.ToCol)}
		bombID, found := match.wireBombID(moved.BombPlayerID, moved.BombTime, source)
		if !found {
			match.logf("kicked bubble of %d at %d,%d is unknown", moved.BombPlayerID, source.Row, source.Col)
			return nil
		}
		accepted, err := match.runtime.AcceptNativeBombMovement(moved.PlayerID, bombID, source, target)
		if err != nil {
			match.logf("bubble kick rejected: %v", err)
			return nil
		}
		match.moveWireBomb(bombID, target)
		delete(match.sceneRequests, sceneRequestKey{schema: game.RequestMoveBomb, object: bombID, from: source, to: target})
		return []battleengine.Event{accepted}
	case game.NotifyPlayerUseItem:
		// The server publishes its AIs' item uses on the scene channel; human
		// uses arrive as TCP notifications (handleTCP).
		if len(message.Data) >= 2 && uint16(message.Data[0])<<8|uint16(message.Data[1]) == sender {
			match.applyItemUse(message.Data)
		}
	case game.NotifyBombExplode:
		if sender != match.arbitrator || match.isArbitrator() {
			return nil
		}
		return match.applyExplosion(event)
	case game.RequestGetBun, game.RequestPutBun:
		request, err := game.ParseBunActionEvent(event)
		if err == nil && match.carry != nil && match.isArbitrator() && request.PlayerID == sender {
			match.arbitrateBun(schema, request)
		}
	case game.NotifyPlayerGetBun, game.NotifyPlayerPutBun:
		notice, err := game.ParseBunActionEvent(event)
		if err == nil && match.carry != nil && sender == match.arbitrator && !match.isArbitrator() {
			match.applyBun(schema, notice)
		}
	case game.NotifyPlayerRelive:
		relive, err := game.ParsePlayerReliveEvent(event)
		if err != nil || sender != match.arbitrator || match.isArbitrator() || match.respawn == nil {
			return nil
		}
		respawned, err := match.engine.RespawnActor(relive.PlayerID, battleengine.Cell{Row: int16(relive.Row), Col: int16(relive.Col)})
		if err != nil {
			match.logf("relive of %d rejected: %v", relive.PlayerID, err)
			return nil
		}
		delete(match.eliminatedAt, relive.PlayerID)
		match.logf("player %d relives at %d,%d", relive.PlayerID, relive.Row, relive.Col)
		return []battleengine.Event{respawned}
	}
	return nil
}

// noteEliminations remembers when each player died, for respawnDue.
func (match *onlineMatch) noteEliminations(events []battleengine.Event) {
	if match.respawn == nil {
		return
	}
	for _, event := range events {
		if event.Kind == battleengine.EventActorEliminated && !match.departed[event.TargetID] {
			match.eliminatedAt[event.TargetID] = event.TimeMS
		}
	}
}

// respawnDue is the arbitrator's relive component (0x00615AAE): a player
// dead for the rule's delay relives at its spawn cell. Under rules 4 and 5 a
// map element on that cell (scene grid +0xE78, the layer pushed boxes move
// in, see 0x00604208) holds back the whole pass until a later frame; players
// and bubbles do not. The NOTIFY_PLAYER_RELIVE goes to every peer and to the
// server.
func (match *onlineMatch) respawnDue() []battleengine.Event {
	if match.respawn == nil || !match.isArbitrator() || match.engine.Terminal().Ended {
		return nil
	}
	now := match.engine.ElapsedMS()
	var events []battleengine.Event
	for _, actor := range match.engine.Actors() {
		diedAt, dead := match.eliminatedAt[actor.PlayerID]
		if !dead || actor.State != battleengine.ActorEliminated || now-diedAt < match.respawn.delayMS {
			continue
		}
		if match.respawn.waitFree && match.spawnBlocked(actor.Spawn) {
			return events
		}
		respawned, err := match.engine.RespawnActor(actor.PlayerID, actor.Spawn)
		if err != nil {
			match.logf("relive of %d: %v", actor.PlayerID, err)
			continue
		}
		delete(match.eliminatedAt, actor.PlayerID)
		match.logf("player %d relives at %d,%d", actor.PlayerID, actor.Spawn.Row, actor.Spawn.Col)
		body, err := (game.PlayerReliveEvent{PlayerID: actor.PlayerID, ClientTime: now, Row: byte(actor.Spawn.Row), Col: byte(actor.Spawn.Col)}).MarshalNetworkBinary()
		if err == nil {
			match.queue(game.NotifyPlayerRelive, now, now, body)
			match.mirror(game.NotifyPlayerRelive, body)
		}
		events = append(events, respawned)
	}
	return events
}

// spawnBlocked reports a map element on the cell. The engine merges both
// map layers into one tile; spawn cells start empty, so only an element
// moved or placed there later sets it.
func (match *onlineMatch) spawnBlocked(cell battleengine.Cell) bool {
	tile, inside := match.engine.TileAt(cell)
	return !inside || tile.Kind != battleengine.CellOpen || tile.MapElementOccupied
}

// wireBombID finds the engine bubble peers name by owner, placement time and
// cell.
func (match *onlineMatch) wireBombID(owner uint16, placedAt uint32, cell battleengine.Cell) (uint32, bool) {
	for id, known := range match.bombs {
		if known.owner == owner && known.placedAt == placedAt && known.cell == cell {
			return id, true
		}
	}
	return 0, false
}

func (match *onlineMatch) moveWireBomb(bombID uint32, cell battleengine.Cell) {
	if known, ok := match.bombs[bombID]; ok {
		known.cell = cell
		match.bombs[bombID] = known
	}
}

func (match *onlineMatch) applyExplosion(event game.GameEvent) []battleengine.Event {
	explosion, err := game.ParseBombExplodeEvent(event)
	if err != nil {
		return nil
	}
	bombs := make([]battleengine.VerifiedExplodedBomb, 0, len(explosion.Bombs))
	for _, bomb := range explosion.Bombs {
		cell := battleengine.Cell{Row: int16(bomb.Row), Col: int16(bomb.Column)}
		bombID, _ := match.wireBombID(bomb.PlayerID, bomb.ClientTime, cell)
		delete(match.bombs, bombID)
		if match.sculpture != nil && bomb.BombID == 9 {
			match.sculpture.damage(cell, bomb.ClientTime)
			delete(match.styleNine, bombID)
		}
		bombs = append(bombs, battleengine.VerifiedExplodedBomb{
			BombID: bombID, OwnerID: bomb.PlayerID, Cell: cell,
			BlastRowMin: int16(bomb.RowMin), BlastRowMax: int16(bomb.RowMax), BlastColMin: int16(bomb.ColumnMin), BlastColMax: int16(bomb.ColumnMax),
		})
	}
	hits := make([]battleengine.VerifiedMapElementHit, 0, len(explosion.MapElems))
	for _, element := range explosion.MapElems {
		hits = append(hits, battleengine.VerifiedMapElementHit{MapElementID: uint32(element.MapElementID), Cell: battleengine.Cell{Row: int16(element.Row), Col: int16(element.Column)}})
	}
	items := make([]battleengine.Pickup, 0, len(explosion.Items))
	for _, item := range explosion.Items {
		items = append(items, battleengine.Pickup{SceneID: item.ItemID, Cell: battleengine.Cell{Row: int16(item.Row), Col: int16(item.Column)}, State: battleengine.PickupAvailable})
	}
	events, err := match.runtime.AcceptNativeBombExplosionAt(explosion.ClientTime, bombs, hits, items)
	if err != nil {
		match.logf("explosion rejected: %v", err)
	}
	return events
}

// forgetPickups drops the collection record of a cell when a new item
// appears there (revealed, dropped by the bird or by a dying player).
func (match *onlineMatch) forgetPickups(events []battleengine.Event) {
	for _, event := range events {
		switch event.Kind {
		case battleengine.EventPickupRevealed, battleengine.EventPickupDispatched, battleengine.EventPickupDropped:
			delete(match.collected, pickupKey{scene: event.SceneID, cell: event.Cell})
		}
	}
}

func (match *onlineMatch) isArbitrator() bool {
	return match.arbitrator == match.local
}

// arbitratePickup answers a 0x0FAC request. The local engine collects items
// (and springs bananas and glue) on contact and announces that at once
// (publishEvents), so a request for something the engine already gave to the
// same player needs no answer. Otherwise the item is granted if it is still
// there and refused (PlayerID 0xffff) if not.
func (match *onlineMatch) arbitratePickup(event game.GameEvent) []battleengine.Event {
	request, err := game.ParsePlayerItemEvent(event)
	if err != nil {
		return nil
	}
	position := battleengine.Position{X: int32(request.PosX), Y: int32(request.PosY)}
	key := pickupKey{scene: request.ItemID, cell: position.Cell()}
	answer := request
	var events []battleengine.Event
	if request.ItemID == 42 || request.ItemID == 43 {
		// Stepping on a banana or glue placed with an item.
		contact := fieldContactKey{player: request.PlayerID, action: uint8(request.ItemID), cell: position.Cell()}
		if match.fieldContacts[contact] {
			return nil
		}
		accepted, err := match.runtime.AcceptNativeFieldObjectContact(request.PlayerID, uint8(request.ItemID), position)
		if err != nil {
			answer.PlayerID = 0xffff
			match.logf("field object %d at %d,%d for %d refused: %v", request.ItemID, contact.cell.Row, contact.cell.Col, request.PlayerID, err)
		} else {
			match.fieldContacts[contact] = true
			events = accepted
		}
		match.publishItem(answer)
		return events
	}
	if owner, done := match.collected[key]; done {
		if owner == request.PlayerID {
			return nil
		}
		answer.PlayerID = 0xffff
		match.logf("pickup %d at %d,%d by %d refused: taken by %d", request.ItemID, key.cell.Row, key.cell.Col, request.PlayerID, owner)
	} else if accepted, err := match.runtime.AcceptNativePickup(request.PlayerID, request.ItemID, position); err == nil {
		match.collected[key] = request.PlayerID
		events = append(events, accepted)
	} else {
		answer.PlayerID = 0xffff
		match.logf("pickup %d at %d,%d by %d refused at %d: %v", request.ItemID, key.cell.Row, key.cell.Col, request.PlayerID, match.engine.ElapsedMS(), err)
	}
	match.publishItem(answer)
	return events
}

// arbitrateContact answers a kill (0x0FA8) or rescue (0x0FAA) request when
// the local engine has not resolved that contact on its own yet.
func (match *onlineMatch) arbitrateContact(event game.GameEvent) []battleengine.Event {
	request, err := game.ParsePlayerInteractionEvent(event)
	if err != nil {
		return nil
	}
	target, ok := actorByID(match.engine.Actors(), request.DestinationPlayerID)
	if !ok || target.State != battleengine.ActorTrapped {
		return nil
	}
	position := battleengine.Position{X: int32(request.PosX), Y: int32(request.PosY)}
	var events []battleengine.Event
	if event.Schema == game.RequestSavePlayer {
		accepted, changed, err := match.runtime.AcceptNativeRescue(request.PlayerID, request.DestinationPlayerID, position)
		if err != nil || !changed {
			return nil
		}
		events = []battleengine.Event{accepted}
	} else if events, err = match.runtime.AcceptNativeElimination(request.DestinationPlayerID, request.PlayerID, nil); err != nil {
		return nil
	}
	match.publishEvents(match.engine, events)
	return events
}

// arbitrateMapElementMove answers a push request (0x0FB2). Remote players
// follow their own reported paths in the local engine, so only their request
// moves the element.
func (match *onlineMatch) arbitrateMapElementMove(sender uint16, event game.GameEvent) []battleengine.Event {
	request, err := game.ParseMoveMapElementRequest(event)
	if err != nil || request.PlayerID != sender {
		return nil
	}
	source := battleengine.Cell{Row: int16(request.Row), Col: int16(request.Col)}
	accepted, err := match.runtime.AcceptNativeMapElementMovement(request.PlayerID, request.ElementID, source, battleDirection(request.Direction))
	if err != nil {
		match.logf("push of map element %d by %d refused: %v", request.ElementID, request.PlayerID, err)
		return nil
	}
	match.publishMapElementMoved(accepted, request.Sequence)
	return []battleengine.Event{accepted}
}

// arbitrateBombMove answers a kick request (0x0FB4) with 0x139C.
func (match *onlineMatch) arbitrateBombMove(sender uint16, event game.GameEvent) []battleengine.Event {
	request, err := game.ParseMoveBombEvent(event)
	if err != nil || request.PlayerID != sender {
		return nil
	}
	source := battleengine.Cell{Row: int16(request.FromRow), Col: int16(request.FromCol)}
	target := battleengine.Cell{Row: int16(request.ToRow), Col: int16(request.ToCol)}
	bombID, found := match.wireBombID(request.BombPlayerID, request.BombTime, source)
	if !found {
		match.logf("kick by %d refused: no bubble of %d at %d,%d", request.PlayerID, request.BombPlayerID, source.Row, source.Col)
		return nil
	}
	accepted, err := match.runtime.AcceptNativeBombMovement(request.PlayerID, bombID, source, target)
	if err != nil {
		match.logf("kick by %d refused: %v", request.PlayerID, err)
		return nil
	}
	match.moveWireBomb(bombID, target)
	if body, err := request.MarshalNetworkBinary(); err == nil {
		now := match.engine.ElapsedMS()
		match.queue(game.NotifyPlayerMoveBomb, now, now, body)
		match.mirror(game.NotifyPlayerMoveBomb, body)
	}
	return []battleengine.Event{accepted}
}

// TCP notifications ---------------------------------------------------------

const (
	notifyUDPOKCommand     = 0x0087
	gameEventNotifyCommand = 0x0084
	leaveRoomNotifyCommand = 0x008E // a member left the room: PlayerID, new owner, arbitrator, map (big endian)
)

func (match *onlineMatch) handleTCP(packet tcpPacket) {
	payload, err := hex.DecodeString(packet.Payload)
	if err != nil {
		return
	}
	switch packet.Command {
	case leaveRoomNotifyCommand:
		if len(payload) >= 2 {
			match.depart(uint16(payload[0])<<8 | uint16(payload[1]))
		}
	case gameEventNotifyCommand:
		notify, err := game.ParseNotifyGameEventPayload(payload)
		if err != nil {
			return
		}
		switch notify.Schema {
		case game.NotifyChangeArbitrator:
			// The server repeats the current arbitrator after any departure.
			if len(notify.Body) >= 2 && uint16(notify.Body[0])<<8|uint16(notify.Body[1]) != match.arbitrator {
				match.arbitrator = uint16(notify.Body[0])<<8 | uint16(notify.Body[1])
				match.logf("arbitrator is now %d", match.arbitrator)
				if match.isArbitrator() {
					match.logf("the engine was started as a non-arbitrator; it cannot arbitrate explosions from here on")
				}
			}
		case game.NotifyPlayerUseItem:
			match.applyItemUse(notify.Body)
		case game.NotifyPreparedUseProp:
			match.applyPreparedUse(notify.Body)
		}
	}
}

// depart removes a player who left the room mid-match. Client.exe takes the
// actor out of the scene and runs the rule's slot 8 for it: rule 5 sends its
// gems to the bird (engine Rules.ObjectiveDepartureRecycle), rule 3 returns
// its bun to the house of that colour (0x0060BC28). It never relives.
func (match *onlineMatch) depart(playerID uint16) {
	if playerID == match.local || match.departed[playerID] {
		return
	}
	if err := match.runtime.AcceptHumanDeparture(playerID); err != nil {
		match.logf("departure of %d: %v", playerID, err)
		return
	}
	match.departed[playerID] = true
	delete(match.eliminatedAt, playerID)
	if match.carry != nil {
		match.carry.onDeparture(playerID)
	}
	match.logf("player %d left the match", playerID)
}

// applySculptureItem is a confirmed use of rule 6's held items. Item 48
// places a style-9 bubble at the user's cell on every client without a
// PLAYER_USE_BOMB (0x0060471C, 0x005CDD0A); item 66 puts the carried
// fragment down there (0x005F6AB8). Both are used up (0x0088DE67).
func (match *onlineMatch) applySculptureItem(use game.WorldUseItemEvent) {
	position := battleengine.Position{X: int32(use.PosX), Y: int32(use.PosY)}
	if use.ItemID == sculpturePutDownItem {
		match.sculpture.putDown(use.PlayerID, position)
		return
	}
	match.engine.ConsumeHeldAction(use.PlayerID, sculptureBombItem)
	delete(match.sculpture.bombLeft, use.PlayerID)
	cell := position.Cell()
	placedAt := max(use.ClientTime, battleengine.NativeRoundStartClockMS)
	accepted, err := match.runtime.AcceptHumanBombPlacement(use.PlayerID, cell, sculptureBombPower, 0, placedAt)
	if err != nil {
		match.logf("style-9 bubble of %d rejected: %v", use.PlayerID, err)
		return
	}
	match.bombs[accepted.BombID] = wireBomb{owner: use.PlayerID, placedAt: use.ClientTime, cell: cell}
	match.styleNine[accepted.BombID] = true
}

// applyItemUse applies another player's held-item use (banana, glue, dart,
// ...), which the server validates and broadcasts as 0x0FB0.
func (match *onlineMatch) applyItemUse(body []byte) {
	use, err := game.ParseWorldUseItemEvent(game.GameEvent{Schema: game.NotifyPlayerUseItem, Body: body})
	if err != nil || use.ItemID > 0xff {
		return
	}
	if use.PlayerID == match.local {
		delete(match.useRequested, uint8(use.ItemID))
	}
	key := [3]uint32{uint32(use.PlayerID), use.ClientTime, use.ItemID}
	if match.itemUses[key] {
		return
	}
	match.itemUses[key] = true
	if match.sculpture != nil && (use.ItemID == sculptureBombItem || use.ItemID == sculpturePutDownItem) {
		match.applySculptureItem(use)
		return
	}
	var targetBombID uint32
	if (use.ItemID == 44 || use.ItemID == 46) && use.Flag4 != 0 && use.Flag2 <= 0xffff {
		// Flag1 packs direction, power, row and column of the targeted bubble;
		// Flag2 and Flag3 are its owner and placement time.
		cell := battleengine.Cell{Row: int16((use.Flag1 >> 8) & 0xff), Col: int16(use.Flag1 & 0xff)}
		targetBombID, _ = match.wireBombID(uint16(use.Flag2), use.Flag3, cell)
	}
	events, err := match.runtime.AcceptNativeBattleAction(use.PlayerID, byte(use.ItemID), battleengine.Position{X: int32(use.PosX), Y: int32(use.PosY)}, targetBombID)
	if err != nil {
		match.logf("item %d used by %d rejected: %v", use.ItemID, use.PlayerID, err)
		return
	}
	match.publishEvents(match.engine, events)
}

// requestItemUse asks the server to use one of the local player's held
// items. Throws (44/46) carry their target: direction, bubble power, row and
// column in Flag1, and the targeted bubble's owner and placement time.
func (match *onlineMatch) requestItemUse(action uint8) {
	actor, ok := actorByID(match.engine.Actors(), match.local)
	// The needle (63) only frees a trapped player; nothing else works
	// from inside a bubble.
	if !ok || actor.State == battleengine.ActorEliminated || (actor.State == battleengine.ActorTrapped) != (action == 63) {
		return
	}
	held := false
	for _, slot := range actor.HeldActions {
		held = held || (slot.ActionID == action && slot.Count > 0)
	}
	now := match.engine.ElapsedMS()
	if requested, pending := match.useRequested[action]; !held || pending && now-requested < 1000 {
		return
	}
	use := game.WorldUseItemEvent{
		PlayerID: match.local, ClientTime: now, ItemID: uint32(action),
		PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y),
	}
	if action == 44 || action == 46 {
		cell, bombID, reachable := match.engine.NativeActionProjectileTarget(match.local)
		if !reachable {
			return
		}
		use.Flag1 = uint32(nativeWalkDirection(actor.Facing))<<24 | uint32(uint16(cell.Row)&0xff)<<8 | uint32(uint16(cell.Col)&0xff)
		use.Flag2 = ^uint32(0)
		if bomb, found := bombByID(match.engine.Bombs(), bombID); found && bombID != 0 {
			known := match.bombs[bombID]
			use.Flag1 |= uint32(bomb.Power+1) << 16
			use.Flag2, use.Flag3, use.Flag4 = uint32(known.owner), known.placedAt, 1
		}
	}
	body, err := use.MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.useRequested[action] = now
	match.mirror(game.RequestUseItem, body)
}

// requestPreparedUse asks the server to use one of the local player's room
// props; the server consumes it and answers with 0x1175 to every client.
func (match *onlineMatch) requestPreparedUse(item uint32) {
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok || actor.State != battleengine.ActorActive || match.preparedCount(item) <= 0 {
		return
	}
	now := match.engine.ElapsedMS()
	if requested, pending := match.preparedRequested[item]; pending && now-requested < 1000 {
		return
	}
	use := game.PreparedUsePropEvent{PlayerID: match.local, ItemID: item, PosX: uint16(actor.Position.X), PosY: uint16(actor.Position.Y)}
	body, err := use.MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.preparedRequested[item] = now
	match.mirror(game.RequestPreparedUseProp, body)
}

// applyPreparedUse applies a confirmed room-prop use (0x1175). Only the cure
// (万能药剂) changes the battle; which statuses it ends is inferred from its
// description. The fireworks (20058..20064) are effects only.
func (match *onlineMatch) applyPreparedUse(body []byte) {
	use, err := game.ParsePreparedUsePropEvent(game.GameEvent{Schema: game.NotifyPreparedUseProp, Body: body})
	if err != nil {
		return
	}
	if use.PlayerID == match.local {
		delete(match.preparedRequested, use.ItemID)
		for index := range match.prepared {
			if match.prepared[index].Item == use.ItemID && match.prepared[index].Count > 0 {
				match.prepared[index].Count--
			}
		}
	}
	if use.ItemID == curePropID {
		match.engine.ClearHarmfulMovementStatus(use.PlayerID)
	}
}

func (match *onlineMatch) preparedCount(item uint32) int16 {
	for _, entry := range match.prepared {
		if entry.Item == item {
			return entry.Count
		}
	}
	return 0
}

func (match *onlineMatch) preparedView() []preparedView {
	return append([]preparedView(nil), match.prepared...)
}

// Publishing ----------------------------------------------------------------

// queue adds one local scene message to the next Type-2 batch.
func (match *onlineMatch) queue(schema uint16, timeMS, gameTime uint32, body []byte) {
	match.enqueue(schema, timeMS, gameTime, body, false)
}

// request queues a message for the arbitrator alone, as the server routes
// its AIs' requests.
func (match *onlineMatch) request(schema uint16, timeMS uint32, body []byte) {
	match.enqueue(schema, timeMS, timeMS, body, true)
}

func (match *onlineMatch) enqueue(schema uint16, timeMS, gameTime uint32, body []byte, arbitratorOnly bool) {
	match.messageSeq++
	match.outbound = append(match.outbound, outboundMessage{arbitratorOnly: arbitratorOnly, data: game.BattleMessageData{
		Time: timeMS, DataID: uint32(schema), Sequence: match.messageSeq, Data: body, GameTime: gameTime,
	}})
}

// mirror sends the reliable REQUEST_PLAY copy of a scene message.
func (match *onlineMatch) mirror(schema uint16, body []byte) {
	payload, err := game.MarshalGameEventPayload(match.localUIN, match.clientTag(), schema, body)
	if err != nil {
		match.logf("mirror 0x%04X: %v", schema, err)
		return
	}
	match.tcpOut = append(match.tcpOut, tcpPacket{Command: requestPlayCommand, Payload: hex.EncodeToString(payload)})
}

// flush sends the queued messages as Type-2 multicasts, one 0x0FBD package
// per message: requests to the arbitrator, everything else to every other
// participant.
func (match *onlineMatch) flush() {
	if len(match.outbound) == 0 {
		return
	}
	outbound := match.outbound
	match.outbound = nil
	if !match.presence {
		return
	}
	var everyone, arbitratorOnly []game.BattleMessageData
	for _, message := range outbound {
		if message.arbitratorOnly {
			arbitratorOnly = append(arbitratorOnly, message.data)
		} else {
			everyone = append(everyone, message.data)
		}
	}
	targets := make([]game.LegacyUDPMulticastTarget, 0, len(match.uins))
	for _, playerID := range match.peerIDs() {
		targets = append(targets, game.LegacyUDPMulticastTarget{PlayerID: playerID, UIN: match.uins[playerID]})
	}
	match.multicast(everyone, targets)
	if match.arbitrator != match.local {
		match.multicast(arbitratorOnly, []game.LegacyUDPMulticastTarget{{PlayerID: match.arbitrator, UIN: match.uins[match.arbitrator]}})
	}
}

func (match *onlineMatch) multicast(messages []game.BattleMessageData, targets []game.LegacyUDPMulticastTarget) {
	if len(targets) == 0 {
		return
	}
	for start := 0; start < len(messages); start += 16 {
		chunk := messages[start:min(start+16, len(messages))]
		batch := game.GameplayBatch{GameID: match.begin.GameID}
		for _, message := range chunk {
			batch.Entries = append(batch.Entries, game.GameplayBatchEntry{Index: uint16(message.Sequence), Package: game.GameplayDataPackage{
				PlayerID: match.local, Time: message.Time, GameID: match.begin.GameID,
				MessageIndexes: []uint32{message.Sequence}, Messages: []game.BattleMessageData{message},
			}})
		}
		data, err := game.EncodeQQTPPPGameplayBatch(batch)
		if err != nil {
			match.logf("encode batch: %v", err)
			continue
		}
		match.multicastNumber++
		match.sendControl(game.LegacyUDPControlPacket{
			Header:    match.header(game.LegacyUDPMulticastType, match.multicastNumber),
			Multicast: &game.LegacyUDPMulticastPayload{Targets: targets, Data: data},
		})
	}
}

func (match *onlineMatch) publishItem(item game.PlayerItemEvent) {
	body, err := item.MarshalNetworkBinary()
	if err != nil {
		return
	}
	now := match.engine.ElapsedMS()
	match.queue(game.NotifyPlayerGetItem, now, now, body)
	if item.PlayerID != 0xffff {
		match.mirror(game.NotifyPlayerGetItem, body)
	}
}

func (match *onlineMatch) publishMapElementMoved(event battleengine.Event, sequence uint16) {
	direction, ok := nativeMoveDirection(event.FromCell, event.Cell)
	if !ok {
		return
	}
	now := match.engine.ElapsedMS()
	body, err := (game.MapElementMovedEvent{
		PlayerID: event.PlayerID, ClientTime: now, ElementID: event.ObjectID,
		Row: uint16(event.FromCell.Row), Col: uint16(event.FromCell.Col), Direction: direction, Sequence: sequence,
	}).MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queue(game.NotifyMapElementMoved, now, now, body)
	match.mirror(game.NotifyMapElementMoved, body)
}

func (match *onlineMatch) publishBombKicked(after *battleengine.Engine, event battleengine.Event) {
	body, ok := match.bombMoveBody(after, event)
	if !ok {
		return
	}
	match.moveWireBomb(event.BombID, event.Cell)
	now := after.ElapsedMS()
	match.queue(game.NotifyPlayerMoveBomb, now, now, body)
	match.mirror(game.NotifyPlayerMoveBomb, body)
}

// bombMoveBody is the 0x0FB4/0x139C body of a kick event. Its bubble is
// named the way peers know it.
func (match *onlineMatch) bombMoveBody(snapshot *battleengine.Engine, event battleengine.Event) ([]byte, bool) {
	direction, ok := nativeMoveDirection(event.FromCell, event.Cell)
	known, placed := match.bombs[event.BombID]
	bomb, found := bombByID(snapshot.Bombs(), event.BombID)
	if !ok || !placed || !found {
		return nil, false
	}
	body, err := (game.MoveBombEvent{
		PlayerID: event.PlayerID, ClientTime: uint16(event.TimeMS),
		FromRow: uint16(event.FromCell.Row), FromCol: uint16(event.FromCell.Col), ToRow: uint16(event.Cell.Row), ToCol: uint16(event.Cell.Col),
		Direction: direction, BombPlayerID: known.owner, BombTime: known.placedAt, BombPower: bomb.Power + 1,
	}).MarshalNetworkBinary()
	return body, err == nil
}

// requestPickup asks the arbitrator for an item (or a banana or glue field,
// actions 42/43) the local player stepped on.
func (match *onlineMatch) requestPickup(scene uint32, cell battleengine.Cell, position battleengine.Position) {
	key := pickupKey{scene: scene, cell: cell}
	if _, pending := match.pickupRequests[key]; pending {
		return
	}
	now := match.engine.ElapsedMS()
	body, err := (game.PlayerItemEvent{PlayerID: match.local, ClientTime: now, ItemID: scene, PosX: uint16(position.X), PosY: uint16(position.Y)}).MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.pickupRequests[key] = pickupRequest{body: body, sentAt: now, attempts: 1}
	match.request(game.RequestGetItem, now, body)
}

// retryPickupRequests repeats unanswered pickup requests while the local
// player stays on the item, and forgets them once it leaves or the item is
// gone. A refused or exhausted request is not renewed until the player
// steps on the cell again.
func (match *onlineMatch) retryPickupRequests() {
	if len(match.pickupRequests) == 0 {
		return
	}
	actor, ok := actorByID(match.engine.Actors(), match.local)
	now := match.engine.ElapsedMS()
	for key, request := range match.pickupRequests {
		if !ok || actor.State != battleengine.ActorActive || actor.Position.Cell() != key.cell || !match.pickupAvailable(key) {
			delete(match.pickupRequests, key)
			continue
		}
		if request.attempts >= pickupAttempts || now-request.sentAt < pickupRetryMS {
			continue
		}
		request.attempts++
		request.sentAt = now
		match.pickupRequests[key] = request
		match.request(game.RequestGetItem, now, request.body)
	}
}

func (match *onlineMatch) pickupAvailable(key pickupKey) bool {
	for _, pickup := range match.engine.Pickups() {
		if pickup.SceneID == key.scene && pickup.Cell == key.cell && pickup.State == battleengine.PickupAvailable {
			return true
		}
	}
	for _, object := range match.engine.FieldObjects() {
		if uint32(object.ActionID) == key.scene && object.Cell == key.cell {
			return true
		}
	}
	return false
}

// requestContact asks the arbitrator to finish (0x0FA8) or rescue (0x0FAA)
// a trapped player the local player touched. The reliable copy is what lets
// the server count the kill or rescue.
func (match *onlineMatch) requestContact(event battleengine.Event) {
	schema := uint16(game.RequestKillPlayer)
	if event.Kind == battleengine.EventActorRescueRequested {
		schema = game.RequestSavePlayer
	}
	body, err := (game.PlayerInteractionEvent{PlayerID: match.local, ClientTime: event.TimeMS, DestinationPlayerID: event.TargetID,
		PosX: uint16(event.Position.X), PosY: uint16(event.Position.Y)}).MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.request(schema, event.TimeMS, body)
	match.mirror(schema, body)
}

// reserveSceneRequest limits a push or kick held against the same object to
// one request per sceneRequestPeriod.
func (match *onlineMatch) reserveSceneRequest(key sceneRequestKey, now uint32) bool {
	if last, pending := match.sceneRequests[key]; pending && now-last < sceneRequestPeriod {
		return false
	}
	match.sceneRequests[key] = now
	return true
}

func (match *onlineMatch) requestMapElementMove(event battleengine.Event) {
	direction, ok := nativeMoveDirection(event.FromCell, event.Cell)
	key := sceneRequestKey{schema: game.RequestMoveMapElement, object: event.ObjectID, from: event.FromCell, to: event.Cell}
	if !ok || !match.reserveSceneRequest(key, event.TimeMS) {
		return
	}
	match.mapElemSeq++
	body, err := (game.MoveMapElementRequest{
		PlayerID: match.local, ClientTime: event.TimeMS, ElementID: event.ObjectID,
		Row: byte(event.FromCell.Row), Col: byte(event.FromCell.Col), Direction: direction, Sequence: match.mapElemSeq,
	}).MarshalNetworkBinary()
	if err == nil {
		match.request(game.RequestMoveMapElement, event.TimeMS, body)
	}
}

func (match *onlineMatch) requestBombMove(after *battleengine.Engine, event battleengine.Event) {
	key := sceneRequestKey{schema: game.RequestMoveBomb, object: event.BombID, from: event.FromCell, to: event.Cell}
	body, ok := match.bombMoveBody(after, event)
	if !ok || !match.reserveSceneRequest(key, event.TimeMS) {
		return
	}
	match.request(game.RequestMoveBomb, event.TimeMS, body)
}

// expireLocalTrap pops the local player's bubble after trapMS: the player
// dies (PLAYER_DIE), which is what lets the server end the round.
func (match *onlineMatch) expireLocalTrap() []battleengine.Event {
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok || actor.State != battleengine.ActorTrapped {
		match.trappedAt = 0
		return nil
	}
	now := match.engine.ElapsedMS()
	if match.trappedAt == 0 {
		match.trappedAt = now
	}
	if now-match.trappedAt < match.trapMS || match.engine.Terminal().Ended {
		return nil
	}
	events, err := match.engine.ConfirmTrappedDeath(match.local)
	if err != nil {
		match.logf("local trap death: %v", err)
		return nil
	}
	return events
}

// publishEvents turns the engine events of one tick into scene messages.
// The local player's own facts are always published; shared outcomes only
// by the arbitrator.
func (match *onlineMatch) publishEvents(after *battleengine.Engine, events []battleengine.Event) {
	now := after.ElapsedMS()
	arbitrator := match.isArbitrator()
	var explosion game.BombExplodeEvent
	var dispatched []game.GameItem
	drops := map[uint16][]game.GameItem{}
	for _, event := range events {
		if event.Kind == battleengine.EventPickupDropped {
			drops[event.PlayerID] = append(drops[event.PlayerID], game.GameItem{ItemID: event.SceneID, Row: byte(event.Cell.Row), Col: byte(event.Cell.Col)})
		}
	}
	for _, event := range events {
		switch event.Kind {
		case battleengine.EventPickupRevealed, battleengine.EventPickupDropped:
			match.forgetPickups([]battleengine.Event{event})
		case battleengine.EventBombPlaced:
			if event.PlayerID != match.local {
				continue
			}
			bomb, ok := bombByID(after.Bombs(), event.BombID)
			if !ok {
				continue
			}
			match.bombs[event.BombID] = wireBomb{owner: match.local, placedAt: event.TimeMS, cell: event.Cell}
			property := byte(0)
			if bomb.SceneFourEffect {
				property = 1
			}
			body, err := (game.PlayerUseBombEvent{
				PlayerID: match.local, ClientTime: event.TimeMS, BombID: defaultBombAppearance,
				Row: byte(event.Cell.Row), Column: byte(event.Cell.Col), Power: uint16(bomb.Power) + 1, Property: property,
			}).MarshalNetworkBinary()
			if err == nil {
				match.queue(game.PlayerUseBomb, event.TimeMS, bombGameTime, body)
				match.mirror(game.PlayerUseBomb, body)
			}
		case battleengine.EventBombExploded:
			if !arbitrator {
				continue
			}
			known := match.bombs[event.BombID]
			placedAt := known.placedAt
			if known.owner == 0 {
				placedAt = event.TimeMS
			}
			style := byte(0xff)
			if match.styleNine[event.BombID] {
				// 0x0060EDC6 writes the bubble type; 0x00607C5A acts on 9.
				style = 9
				match.sculpture.damage(event.Cell, placedAt)
				delete(match.styleNine, event.BombID)
			}
			explosion.Bombs = append(explosion.Bombs, game.ExplodedBomb{
				PlayerID: event.PlayerID, ClientTime: placedAt, BombID: style,
				Row: byte(event.Cell.Row), Column: byte(event.Cell.Col),
				RowMin: byte(event.BlastRowMin), RowMax: byte(event.BlastRowMax), ColumnMin: byte(event.BlastColMin), ColumnMax: byte(event.BlastColMax),
			})
			delete(match.bombs, event.BombID)
		case battleengine.EventCellDamaged, battleengine.EventCellDestroyed:
			// Destroyed walls start their 500 ms retirement in the blast tick.
			if arbitrator && event.ObjectID != 0 {
				explosion.MapElems = append(explosion.MapElems, game.ExplodedMapElement{MapElementID: uint16(event.ObjectID), Row: byte(event.Cell.Row), Column: byte(event.Cell.Col)})
			}
		case battleengine.EventPickupDestroyed:
			if arbitrator {
				explosion.Items = append(explosion.Items, game.ExplodedItem{ItemID: event.SceneID, Row: byte(event.Cell.Row), Column: byte(event.Cell.Col)})
			}
		case battleengine.EventPickupDispatched:
			match.forgetPickups([]battleengine.Event{event})
			if arbitrator {
				dispatched = append(dispatched, game.GameItem{ItemID: event.SceneID, Row: byte(event.Cell.Row), Col: byte(event.Cell.Col)})
			}
		case battleengine.EventActorTrapped:
			if event.TargetID == match.local {
				match.publishHit(event, false)
			}
		case battleengine.EventActorTransformationEnded:
			if event.PlayerID == match.local && event.TransformationEnd == battleengine.TransformationEndHit {
				match.publishHit(event, true)
			}
			if arbitrator && event.TransformationEnd == battleengine.TransformationEndExpired {
				body, err := (game.AvatarRecoveryEvent{PlayerID: event.PlayerID, Time: now, PosX: uint16(event.Position.X), PosY: uint16(event.Position.Y)}).MarshalNetworkBinary()
				if err == nil {
					match.queue(game.NotifyRecoverAvatar, now, now, body)
					match.mirror(game.NotifyRecoverAvatar, body)
				}
			}
		case battleengine.EventPickupCollected:
			key := pickupKey{scene: event.SceneID, cell: event.Cell}
			if _, granted := match.collected[key]; granted {
				continue // granted on request (arbitratePickup), already announced
			}
			match.collected[key] = event.PlayerID
			if arbitrator {
				position := event.Position
				if actor, ok := actorByID(after.Actors(), event.PlayerID); ok && position == (battleengine.Position{}) {
					position = actor.Position
				}
				match.publishItem(game.PlayerItemEvent{PlayerID: event.PlayerID, ClientTime: now, ItemID: event.SceneID, PosX: uint16(position.X), PosY: uint16(position.Y)})
			}
		case battleengine.EventFieldObjectTriggered:
			if event.ActionID != 42 && event.ActionID != 43 {
				continue
			}
			if !arbitrator {
				// Stepping on a banana or glue is asked for like a pickup.
				if event.TargetID == match.local {
					match.requestPickup(uint32(event.ActionID), event.Cell, event.Position)
				}
				continue
			}
			contact := fieldContactKey{player: event.TargetID, action: event.ActionID, cell: event.Cell}
			if !match.fieldContacts[contact] {
				match.publishItem(game.PlayerItemEvent{PlayerID: event.TargetID, ClientTime: now, ItemID: uint32(event.ActionID),
					PosX: uint16(event.Position.X), PosY: uint16(event.Position.Y)})
			}
			match.fieldContacts[contact] = true
		case battleengine.EventPickupCollectRequested:
			if event.PlayerID == match.local {
				match.requestPickup(event.SceneID, event.Cell, event.Position)
			}
		case battleengine.EventActorEliminationRequested, battleengine.EventActorRescueRequested:
			if event.PlayerID == match.local {
				match.requestContact(event)
			}
		case battleengine.EventActorHitRequested:
			// A capture field (41): the local player's own hit.
			if event.PlayerID == match.local {
				isAvatar := event.SceneID != 0
				if _, changed, err := match.engine.ApplyVerifiedActorHit(match.local, event.Position, isAvatar); err == nil && changed {
					match.publishHit(event, isAvatar)
				}
			}
		case battleengine.EventMapElementMoveRequested:
			if event.PlayerID == match.local {
				match.requestMapElementMove(event)
			}
		case battleengine.EventBombKickRequested:
			if event.PlayerID == match.local {
				match.requestBombMove(after, event)
			}
		case battleengine.EventMapElementMoved:
			if arbitrator {
				match.mapElemSeq++
				match.publishMapElementMoved(event, match.mapElemSeq)
			}
		case battleengine.EventBombKicked:
			if arbitrator {
				match.publishBombKicked(after, event)
			}
		case battleengine.EventActorRescued:
			// A needle self-rescue (PlayerID == TargetID) is already known to
			// everyone from the item use.
			if arbitrator && event.PlayerID != event.TargetID {
				body, err := (game.PlayerInteractionEvent{PlayerID: event.PlayerID, ClientTime: now, DestinationPlayerID: event.TargetID,
					PosX: uint16(event.Position.X), PosY: uint16(event.Position.Y)}).MarshalNetworkBinary()
				if err == nil {
					match.queue(game.NotifyPlayerSaved, now, now, body)
					match.mirror(game.NotifyPlayerSaved, body)
				}
			}
		case battleengine.EventActorEliminated:
			match.publishElimination(event, drops[event.TargetID], arbitrator)
		}
	}
	if len(explosion.Bombs) > 0 {
		explosion.PlayerID, explosion.ClientTime = match.local, now
		if body, err := explosion.MarshalNetworkBinary(); err == nil {
			match.queue(game.NotifyBombExplode, now, now, body)
			match.mirror(game.NotifyBombExplode, body)
		} else {
			match.logf("explosion: %v", err)
		}
	}
	if len(dispatched) > 0 {
		if body, err := (game.DispatchItemData{Time: now, Items: dispatched}).MarshalNetworkBinary(); err == nil {
			match.queue(game.NotifyDispatchItem, now, now, body)
			match.mirror(game.NotifyDispatchItem, body)
		}
	}
}

func (match *onlineMatch) publishHit(event battleengine.Event, isAvatar bool) {
	position := event.Position
	body, err := (game.PlayerExplodedEvent{PlayerID: match.local, ClientTime: event.TimeMS, PosX: uint16(position.X), PosY: uint16(position.Y), IsAvatar: isAvatar}).MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queue(game.PlayerBeExploded, event.TimeMS, event.TimeMS, body)
	match.mirror(game.PlayerBeExploded, body)
	// The native client stops and faces down with a forced movement flush.
	match.movement.sequence++
	match.movement.moving, match.movement.direction = false, battleengine.DirectionDown
	match.movement.lastSentAt, match.movement.lastSentPosition = event.TimeMS, position
	match.sendMove(game.PlayerMoveSequence{
		Sequence: match.movement.sequence, TimeStamp: event.TimeMS,
		CurrentPosX: uint16(position.X), CurrentPosY: uint16(position.Y), EndPosX: uint16(position.X), EndPosY: uint16(position.Y),
		WalkAndDirection: nativeWalkDirection(battleengine.DirectionDown) | 0x20, Speed: match.movement.speed,
	})
}

func (match *onlineMatch) publishElimination(event battleengine.Event, drops []game.GameItem, arbitrator bool) {
	now := event.TimeMS
	if drops == nil {
		drops = []game.GameItem{}
	}
	if event.EliminationCause == battleengine.EliminationTrapDeath {
		if event.TargetID != match.local {
			return
		}
		body, err := (game.PlayerDeathEvent{PlayerID: match.local, ClientTime: now, PosX: uint16(event.Position.X), PosY: uint16(event.Position.Y), Items: drops}).MarshalNetworkBinary()
		if err == nil {
			match.queue(game.NotifyPlayerDieEvent, now, now, body)
			match.mirror(game.NotifyPlayerDieEvent, body)
		}
		if match.carry != nil {
			match.carry.onDeath(match.local, 0, false, event.Position)
		}
		return
	}
	if !arbitrator || event.PlayerID == 0 {
		return
	}
	body, err := (game.PlayerKilledEvent{
		PlayerInteractionEvent: game.PlayerInteractionEvent{PlayerID: event.PlayerID, ClientTime: now, DestinationPlayerID: event.TargetID,
			PosX: uint16(event.Position.X), PosY: uint16(event.Position.Y)},
		Items: drops,
	}).MarshalNetworkBinary()
	if err == nil {
		match.queue(game.NotifyPlayerKilled, now, now, body)
		match.mirror(game.NotifyPlayerKilled, body)
	}
	if match.carry != nil {
		match.carry.onDeath(event.TargetID, event.PlayerID, true, event.Position)
	}
}

// publishMovement emits PLAYER_MOVE samples for the local player the way the
// server's AIs do (competitive_ai_runtime.go): on every segment boundary or
// path change, plus a heartbeat every 150 ms. Ordinary movement is published
// at the start of the tick, where the actor actually turns.
func (match *onlineMatch) publishMovement(before, after *battleengine.Engine, action battleengine.Action) {
	current, ok := actorByID(after.Actors(), match.local)
	if !ok || current.State == battleengine.ActorEliminated {
		return
	}
	moving, direction := movementIntent(action, current)
	if before != nil && (!match.movement.initialized || match.movement.lastSentAt < before.ElapsedMS()) {
		previous, previousOK := actorByID(before.Actors(), match.local)
		beforeSpeed, _ := before.NativeEffectiveSpeedRate(match.local, direction)
		afterSpeed, _ := after.NativeEffectiveSpeedRate(match.local, direction)
		if previousOK && previous.State == battleengine.ActorActive && current.State == battleengine.ActorActive &&
			previous.MovementStatus == current.MovementStatus && previous.TransformationSceneID == current.TransformationSceneID && beforeSpeed == afterSpeed {
			match.sampleMovement(before, previous, moving, direction)
			return
		}
	}
	match.sampleMovement(after, current, moving, direction)
}

func (match *onlineMatch) sampleMovement(snapshot *battleengine.Engine, actor battleengine.Actor, moving bool, direction battleengine.Direction) {
	projection := &match.movement
	speed, ok := snapshot.NativeEffectiveSpeedRate(actor.PlayerID, direction)
	if !ok {
		return
	}
	boundary := !projection.initialized || moving != projection.moving || (moving && direction != projection.direction) || speed != projection.speed
	if boundary {
		projection.sequence++
	}
	path := battleengine.NativeMovementProjection{End: actor.Position}
	if moving {
		projected, err := snapshot.ProjectNativeMovement(actor.PlayerID, direction)
		if err != nil {
			return
		}
		path = projected
		if !boundary && path.End == projection.path.End && !path.HasCorner {
			path.Corner, path.HasCorner = projection.path.Corner, projection.path.HasCorner
		}
	}
	now := snapshot.ElapsedMS()
	pathChanged := path != projection.path
	due := boundary || pathChanged || now-projection.lastSentAt >= movementHeartbeat
	if !due || (!boundary && !pathChanged && projection.lastSentAt == now && projection.lastSentPosition == actor.Position) {
		return
	}
	projection.initialized, projection.moving, projection.direction, projection.speed, projection.path = true, moving, direction, speed, path
	projection.lastSentAt, projection.lastSentPosition = now, actor.Position
	walk := nativeWalkDirection(direction)
	if moving {
		walk |= 0x10
	}
	grid := snapshot.Grid()
	end := battleengine.Position{
		X: clamp(path.End.X, int32(grid.Width)*battleengine.CellSizePixels-1),
		Y: clamp(path.End.Y, int32(grid.Height)*battleengine.CellSizePixels-1),
	}
	var corner battleengine.Position
	if path.HasCorner {
		corner = path.Corner
	}
	match.sendMove(game.PlayerMoveSequence{
		Sequence: projection.sequence, TimeStamp: now,
		CurrentPosX: uint16(actor.Position.X), CurrentPosY: uint16(actor.Position.Y),
		CornerPosX: uint16(corner.X), CornerPosY: uint16(corner.Y), EndPosX: uint16(end.X), EndPosY: uint16(end.Y),
		WalkAndDirection: walk, Speed: speed,
	})
}

func (match *onlineMatch) sendMove(move game.PlayerMoveSequence) {
	body, err := (game.PlayerMove{PlayerID: match.local, Entries: []game.PlayerMoveEntry{{PlayerID: match.local, Move: move}}}).MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.queue(game.PlayerMoveSchema, move.TimeStamp, move.TimeStamp, body)
}

func movementIntent(action battleengine.Action, actor battleengine.Actor) (bool, battleengine.Direction) {
	if actor.State != battleengine.ActorActive || action.Move == battleengine.DirectionNone {
		return actor.MovementStatus == battleengine.MovementStatusForcedSlide && actor.State == battleengine.ActorActive, actor.Facing
	}
	return true, actor.Facing
}

// Wire direction order is right, up, left, down.
func nativeWalkDirection(direction battleengine.Direction) byte {
	switch direction {
	case battleengine.DirectionRight:
		return 0
	case battleengine.DirectionUp:
		return 1
	case battleengine.DirectionLeft:
		return 2
	default:
		return 3
	}
}

// nativeMoveDirection is the wire direction of a straight move between two
// cells.
func nativeMoveDirection(from, to battleengine.Cell) (byte, bool) {
	switch {
	case to.Row == from.Row && to.Col > from.Col:
		return 0, true
	case to.Col == from.Col && to.Row < from.Row:
		return 1, true
	case to.Row == from.Row && to.Col < from.Col:
		return 2, true
	case to.Col == from.Col && to.Row > from.Row:
		return 3, true
	default:
		return 0, false
	}
}

func battleDirection(walk byte) battleengine.Direction {
	return [4]battleengine.Direction{battleengine.DirectionRight, battleengine.DirectionUp, battleengine.DirectionLeft, battleengine.DirectionDown}[walk&3]
}

func clamp(value, maximum int32) int32 {
	return max(0, min(value, maximum))
}

func actorByID(actors []battleengine.Actor, playerID uint16) (battleengine.Actor, bool) {
	for _, actor := range actors {
		if actor.PlayerID == playerID {
			return actor, true
		}
	}
	return battleengine.Actor{}, false
}

func bombByID(bombs []battleengine.Bomb, bombID uint32) (battleengine.Bomb, bool) {
	for _, bomb := range bombs {
		if bomb.ID == bombID {
			return bomb, true
		}
	}
	return battleengine.Bomb{}, false
}

func pickupsFromItems(items []game.GameItem) []battleengine.Pickup {
	pickups := make([]battleengine.Pickup, 0, len(items))
	for _, item := range items {
		pickups = append(pickups, battleengine.Pickup{SceneID: item.ItemID, Cell: battleengine.Cell{Row: int16(item.Row), Col: int16(item.Col)}, State: battleengine.PickupAvailable})
	}
	return pickups
}

// Carry rules (3 and 6) ------------------------------------------------------

// carryTick sends the local player's take or put request, at most once per
// bunRequestPeriod while the same request stands. The arbitrator answers its
// own player at once.
func (match *onlineMatch) carryTick() {
	if match.carry == nil || !match.localActive() {
		return
	}
	actor, ok := actorByID(match.engine.Actors(), match.local)
	if !ok {
		return
	}
	schema, request, wanted := match.carry.wanted(actor)
	if !wanted {
		return
	}
	now := match.engine.ElapsedMS()
	request.ClientTime = now
	same := schema == match.carryRequestKind && request.BunID == match.carryRequest.BunID && request.BunTeamID == match.carryRequest.BunTeamID
	if same && now-match.carryRequestSent < bunRequestPeriod {
		return
	}
	match.carryRequest, match.carryRequestKind, match.carryRequestSent = request, schema, now
	if match.isArbitrator() {
		match.arbitrateBun(schema, request)
		return
	}
	if body, err := request.MarshalNetworkBinary(); err == nil {
		match.request(schema, now, body)
	}
}

// arbitrateBun answers a take or put request as 0x00603D53 and 0x00603B0F
// do: a valid one is applied and announced to every peer and the server; an
// invalid one gets no answer.
func (match *onlineMatch) arbitrateBun(schema uint16, request game.BunActionEvent) {
	actor, ok := actorByID(match.engine.Actors(), request.PlayerID)
	if !ok || actor.State != battleengine.ActorActive {
		return
	}
	notice := uint16(game.NotifyPlayerGetBun)
	if schema == game.RequestPutBun {
		if !match.carry.allowPut(request, actor) {
			return
		}
		notice = game.NotifyPlayerPutBun
	} else if !match.carry.allowTake(request, actor) {
		return
	}
	body, err := request.MarshalNetworkBinary()
	if err != nil {
		return
	}
	match.applyBun(notice, request)
	now := match.engine.ElapsedMS()
	match.queue(notice, now, now, body)
	match.mirror(notice, body)
}

func (match *onlineMatch) applyBun(schema uint16, notice game.BunActionEvent) {
	if schema == game.NotifyPlayerPutBun {
		match.carry.applyPut(notice)
		match.logf("player %d puts %d home (%d) at %d,%d", notice.PlayerID, notice.BunTeamID, notice.BunID, notice.PosX, notice.PosY)
		if match.sculpture != nil {
			match.logf("statues: %d/%d", match.sculpture.statues[0].progress, match.sculpture.statues[1].progress)
		}
	} else {
		match.carry.applyTake(notice)
		match.logf("player %d takes %d (%d)", notice.PlayerID, notice.BunTeamID, notice.BunID)
	}
}
