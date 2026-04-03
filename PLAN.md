# Spec : boutique on-chain avec Circle Gateway, paiement gasless, yield et refund cross-chain

## Contexte

POC de boutique on-chain dont la trésorerie est centrée sur Circle Gateway :

1. Le client paie en USDC depuis une chain supportée (gasless via ERC-3009).
2. La boutique accumule une balance Gateway unifiée.
3. Une partie de cette balance est sweep vers un vault de yield sur Base.
4. La boutique peut rembourser instantanément un client sur une autre chain.

---

## Décisions structurantes

### D1. Paiement entrant : ERC-3009 en primaire

Le flux primaire utilise une autorisation USDC `ERC-3009` signée off-chain, puis relayée on-chain par le backend.

Pourquoi :

- Vrai first-use gasless sur USDC — aucune approval préalable nécessaire
- Le backend relaie la tx sans que `msg.sender` soit le client
- Permit2 nécessite une approval ERC20 initiale (confirmé) → fallback optionnel uniquement

### D2. Gateway : primitives réelles

Le client Gateway est construit autour des vraies primitives Circle :

- `TransferSpec` (14 champs, types vérifiés)
- `BurnIntent` (3 champs)
- `depositFor(address token, address depositor, uint256 value)`
- `/v1/estimate` pour `maxFee` et `maxBlockHeight`

### D3. Sweep : composition explicite via TreasuryComposer

`GatewayMinter.gatewayMint(bytes,bytes)` ne prend que attestation + signature. hookData est dans le TransferSpec encodé mais **n'est pas exécuté** par le minter (confirmé — CCTP : « Hook execution is left entirely to the integrator »).

Le sweep vers le vault passe par un contrat de composition dédié sur Base :

- `TreasuryComposer.mintAndDeposit(...)` appelle `gatewayMint(...)` puis dépose l'USDC reçu dans le vault

### D4. Refund : Forwarding Service

Le Forwarding Service gère le mint destination automatiquement :

- `POST /v1/transfer?enableForwarder=true`
- polling `GET /v1/transfer/{id}`
- Pas de gas ni de wallet requis sur la destination

### D5. Confirmation de paiement en 2 phases

1. Succès on-chain du `PaymentRouter` → event `PaymentReceived`
2. Disponibilité réelle dans Gateway via `/v1/deposits` puis `/v1/balances`

Un event `PaymentReceived` ne prouve pas que la balance unifiée est spendable.

---

## Contraintes réseau

Chains de démo : **Base Sepolia** (domain 6) et **Unichain Sepolia** (domain 10).

Finalité Gateway ~20 min sur ces testnets. Non bloquant pour la démo : les étapes longues seront démontrées avec des calls pré-exécutés.

---

## Architecture

```
CLIENT (wallet)
  │
  │ signe ERC-3009 auth off-chain
  ▼
BACKEND  POST /api/pay
  │
  │ relaie la tx, paie le gas
  ▼
PaymentRouter (source chain)
  1. usdc.receiveWithAuthorization(from, this, value, ...)
  2. gatewayWallet.depositFor(usdc, shop, amount)
  3. emit PaymentReceived(orderId, from, value)
  │
  ▼
Gateway Wallet ──► Gateway offchain ledger (balance unifiée)
  │
  ├──► F5 watcher
  │      écoute PaymentReceived on-chain
  │      poll /v1/deposits + /v1/balances
  │      log: router_confirmed → gateway_available
  │
  ├──► F7 sweep (vers Base)
  │      burn intent(s) → attestation
  │      → TreasuryComposer.mintAndDeposit(attestation, sig)
  │        1. gatewayMint(attestation, sig) → USDC arrive
  │        2. vault.deposit(delta) ou pool.supply(delta)
  │
  └──► F8 refund (vers client, any chain)
         burn intent(s) → /v1/transfer?enableForwarder=true
         poll /v1/transfer/{id} → client reçoit USDC
```

---

## Feature map

```
F1: Gateway client + types + signer
F2: Gateway smoke tests
  └── depends on F1
F3: PaymentRouter contract (ERC-3009 primary)
F4: Relayer backend
  └── depends on F3
F5: Payment watcher
  └── depends on F3, F4
F6: TreasuryComposer contract + vault
  └── depends on F1
F7: Sweep script (Gateway → Base → vault)
  └── depends on F1, F2, F6
F8: Refund script
  └── depends on F1, F2
```

---

## F1 — Gateway client + types + signer

Package Go encapsulant les appels Gateway API, les types, la signature EIP-712, le mint self-managed et le polling du Forwarding Service.

### Endpoints

- `GET /v1/info`
- `POST /v1/balances`
- `GET /v1/deposits`
- `POST /v1/estimate`
- `POST /v1/transfer`
- `GET /v1/transfer/{id}`

### Types

#### TransferSpec

```
version (uint32), sourceDomain (uint32), destinationDomain (uint32),
sourceContract (bytes32), destinationContract (bytes32),
sourceToken (bytes32), destinationToken (bytes32),
sourceDepositor (bytes32), destinationRecipient (bytes32),
sourceSigner (bytes32), destinationCaller (bytes32),
value (uint256), salt (bytes32), hookData (bytes)
```

#### BurnIntent

```
maxBlockHeight (uint256), maxFee (uint256), spec (TransferSpec)
```

### Signature EIP-712

Domain complet :

```json
{
  "name": "GatewayWallet",
  "version": "1",
  "chainId": "<from /v1/info or hardcoded per chain>",
  "verifyingContract": "<gateway_wallet_address per chain>"
}
```

`chainId` et `verifyingContract` : récupérer via `/v1/info` (à valider en étape 0). Fallback : hardcoder par chain.

Le builder gère le padding `address → bytes32` (left-pad zeros, standard ABI).

### `/v1/estimate` — fallback prévu

L'endpoint est documenté pour le Forwarding Service. Non vérifié pour les transfers self-managed. Prévoir :

- Appeler `/v1/estimate` en premier
- Si 404 ou non supporté : fallback `maxFee = 2_010000` (2 USDC), `maxBlockHeight = type(uint256).max`
- Ces valeurs viennent des exemples officiels Circle

### Interface cible

- `NewGatewayClient(config) → GatewayClient`
- `client.GetInfo(ctx) → GatewayInfo`
- `client.GetBalances(ctx, req) → GatewayBalances`
- `client.GetDeposits(ctx, req) → GatewayDeposits`
- `client.Estimate(ctx, specs, opts) → EstimateResponse`
- `client.BuildTransferSpec(params) → TransferSpec`
- `client.SignBurnIntent(intent) → signature`
- `client.Transfer(ctx, intents, opts) → TransferResponse`
- `client.GetTransferStatus(ctx, transferID) → TransferStatus`
- `client.SubmitMint(ctx, chainRPC, attestation, signature) → txHash`

### Règles d'implémentation

- Récupérer les adresses wallet/minter/token via `/v1/info`
- Supporter les burn intent sets EVM pour agréger plusieurs sources si la balance est fragmentée
- Exposer `enableForwarder=true` comme option au niveau client
- Timeout de 30 min sur tous les pollings

### Validation

```bash
# 1. Unit tests Go
go test ./internal/gateway/...

# 2. Vérifier que /v1/info retourne des données cohérentes
go run cmd/gateway-smoke-forwarder/main.go --info-only
# attendu : walletAddress, minterAddress, tokenAddress non vides

# 3. Vérifier le padding address → bytes32
# test unitaire : pad(0xdead...beef) == 0x000...0dead...beef (32 bytes)

# 4. Vérifier la signature EIP-712
# test unitaire : signer un BurnIntent connu, vérifier que le hash
# et la signature correspondent à une référence calculée manuellement

# 5. Vérifier /v1/estimate
go run cmd/gateway-smoke-forwarder/main.go --estimate-only
# attendu : maxFee > 0, maxBlockHeight > 0
# si 404 : vérifier que le fallback hardcodé est utilisé
```

---

## F2 — Gateway smoke tests

Valider la plomberie Gateway avant toute feature produit.

### Smoke test A : self-managed mint

1. Déposer du test USDC avec `gatewayWallet.deposit(...)` ou `depositFor(...)`
2. Attendre la finalité Gateway
3. Vérifier `/v1/balances`
4. Construire un transfer simple Base Sepolia → Unichain Sepolia
5. Signer le burn intent
6. Appeler `/v1/estimate` (si dispo, sinon fallback hardcodé)
7. Appeler `/v1/transfer`
8. Soumettre `gatewayMint(attestation, signature)` sur la destination
9. Vérifier le mint

### Smoke test B : forwarding service

1. Construire un transfer simple avec `enableForwarder=true`
2. Appeler `/v1/estimate?enableForwarder=true`
3. Appeler `/v1/transfer?enableForwarder=true`
4. Poller `GET /v1/transfer/{id}` jusqu'à `confirmed` ou `finalized`

### Règles

- Ne jamais transférer l'USDC au Gateway Wallet via un simple `ERC20.transfer`
- Logger chaque étape avec domaines, montants, fees, tx hashes et délais
- Timeout 30 min sur tous les pollings

### Résultat attendu

- `cmd/gateway-smoke-selfmint/main.go`
- `cmd/gateway-smoke-forwarder/main.go`

### Validation

```bash
# Smoke test B (forwarding) — exécuter en premier car plus simple
go run cmd/gateway-smoke-forwarder/main.go \
  --from-domain 6 --to-domain 10 --amount 1000000
# attendu :
#   - /v1/transfer retourne un transferId
#   - polling /v1/transfer/{id} passe par pending → confirmed
#   - balance USDC du destinataire augmente sur Unichain Sepolia
#   - temps total loggé (noter pour calibrer la démo)

# Smoke test A (self-managed mint)
go run cmd/gateway-smoke-selfmint/main.go \
  --from-domain 6 --to-domain 10 --amount 1000000
# attendu :
#   - deposit visible dans /v1/balances après finalité
#   - /v1/transfer retourne attestation
#   - gatewayMint tx success sur destination
#   - USDC arrivé au destinataire
#   - temps total loggé par étape
```

---

## F3 — PaymentRouter contract

Recevoir un paiement USDC signé off-chain par le client, puis le déposer dans Gateway au nom de la boutique.

### Adresses USDC

- Base Sepolia : `0x036CbD53842c5426634e7929541eC2318f3dCF7e`
- Unichain Sepolia : `0x31d0220469e10c4E71834a79b1f276d740d3768F`

### Interface

```solidity
function payWithAuthorization(
    address from,
    uint256 value,
    uint256 validAfter,
    uint256 validBefore,
    bytes32 nonce,
    uint8 v,
    bytes32 r,
    bytes32 s,
    bytes32 orderId
) external;
```

### Flow interne

1. `usdc.receiveWithAuthorization(from, address(this), value, validAfter, validBefore, nonce, v, r, s)`
2. `gatewayWallet.depositFor(address(usdc), shopAddress, value)`
3. emit `PaymentReceived(orderId, from, value)`

### Allowance

`depositFor()` tire l'USDC du caller via `transferFrom`. Le PaymentRouter doit avoir approuvé le GatewayWallet.

**Dans le constructor** : `usdc.approve(gatewayWallet, type(uint256).max)`.

### Tests Foundry

- Vérifier que `PaymentReceived` est émis
- Vérifier que le router finit avec 0 USDC
- Vérifier que l'approve vers GatewayWallet existe

### Validation

```bash
# 1. Tests Foundry
forge test --match-contract PaymentRouterTest -vvv
# attendu :
#   - event PaymentReceived(orderId, from, value) émis
#   - router.balanceOf(usdc) == 0 après le call
#   - allowance(router, gatewayWallet) == type(uint256).max

# 2. Test sur fork Base Sepolia
forge test --fork-url $BASE_SEPOLIA_RPC --match-contract PaymentRouterTest -vvv
# attendu : même résultats avec le vrai contrat USDC

# 3. Déploiement + test manuel
forge script script/DeployPaymentRouter.s.sol --broadcast --rpc-url $BASE_SEPOLIA_RPC
# puis appeler payWithAuthorization avec une signature ERC-3009 valide
# vérifier via cast :
cast call $ROUTER "usdc()" --rpc-url $BASE_SEPOLIA_RPC    # adresse USDC
cast call $USDC "allowance(address,address)(uint256)" $ROUTER $GATEWAY_WALLET --rpc-url $BASE_SEPOLIA_RPC
# attendu : allowance == max uint256
```

### Fallback Permit2 (optionnel)

Si ERC-3009 indisponible sur une chain cible :

- Approval ERC20 initiale vers Permit2 obligatoire
- `owner` du permit passé explicitement
- Ne pas présenter comme "first-use 0 tx"

---

## F4 — Relayer backend

Recevoir la signature de paiement du client et soumettre la tx vers `PaymentRouter`.

### Endpoint

```json
POST /api/pay
{
  "order_id": "0x...",
  "chain_id": 84532,
  "from": "0x...",
  "value": "1000000",
  "valid_after": "0",
  "valid_before": "1735689600",
  "nonce": "0x...",
  "v": 27,
  "r": "0x...",
  "s": "0x..."
}
```

### Exigences

- Allowlist stricte des `chain_id` supportés
- Allowlist stricte des contrats `PaymentRouter`
- **Nonce management** : compteur local par chain. `pendingNonce = max(chainNonce, localCounter)`. Retry avec même nonce si tx échoue. Rate limit 1 tx/sec par chain.
- **Idempotence** : map en mémoire par `order_id` + log append-only NDJSON
- **Replay NDJSON au startup** : lire le log et reconstruire la map `order_id → tx_hash` au démarrage
- Retour d'erreur explicite : distinguer revert pré-inclusion (gas estimation failure) vs revert post-inclusion (on-chain revert)

### Validation

```bash
# 1. Démarrer le relayer
go run cmd/relayer/main.go --port 8080

# 2. Envoyer un paiement valide
curl -X POST http://localhost:8080/api/pay -d '{
  "order_id": "0x0001",
  "chain_id": 84532,
  "from": "0xCLIENT",
  "value": "1000000",
  ...signature fields...
}'
# attendu : 200 OK, tx_hash retourné
# vérifier la tx on-chain via cast ou explorateur

# 3. Tester l'idempotence
curl -X POST http://localhost:8080/api/pay -d '{ "order_id": "0x0001", ... }'
# attendu : 200 OK, même tx_hash retourné (pas de nouvelle tx)

# 4. Tester le replay NDJSON
# arrêter le relayer, redémarrer, renvoyer order_id "0x0001"
# attendu : même tx_hash, pas de double-paiement

# 5. Tester chain_id invalide
curl -X POST http://localhost:8080/api/pay -d '{ "chain_id": 999, ... }'
# attendu : 400 Bad Request, "unsupported chain"

# 6. Vérifier le fichier NDJSON
cat payments.ndjson
# attendu : une ligne par paiement avec order_id, tx_hash, timestamp
```

---

## F5 — Payment watcher

Suivre le paiement de bout en bout : succès on-chain du router puis disponibilité effective dans Gateway.

### Comportement

1. Écouter `PaymentReceived` sur chaque chain
2. Corréler avec `orderId`
3. Poller `/v1/deposits` et `/v1/balances`
4. Logger deux états : `router_confirmed` → `gateway_available`

### Corrélation

Si `/v1/deposits` expose le tx hash source → corrélation directe. Sinon → heuristique montant + timestamp + chain.

### Persistence

- Persister `lastScannedBlock` par chain dans un fichier JSON
- Permet un redémarrage propre sans rescanner depuis le genesis
- Polling HTTP toutes les 5 secondes

### Validation

```bash
# 1. Démarrer le watcher
go run cmd/watcher/main.go

# 2. Déclencher un paiement via F4 (relayer)
# attendu dans les logs du watcher :
#   [router_confirmed] orderId=0x0001 chain=84532 tx=0x... block=12345
#   ... (attendre finalité Gateway) ...
#   [gateway_available] orderId=0x0001 balance_delta=+1000000

# 3. Tester le restart
# arrêter le watcher, vérifier que state.json contient lastScannedBlock
cat state.json
# attendu : {"84532": 12345, "1301": ...}
# redémarrer le watcher → doit reprendre au bon block, pas d'events dupliqués

# 4. Vérifier qu'un paiement non corrélé ne produit pas de faux positif
# déposer directement dans le Gateway Wallet (pas via PaymentRouter)
# attendu : le watcher ne log pas de router_confirmed pour ce dépôt
```

---

## F6 — TreasuryComposer contract + vault

Composer le mint Gateway + le dépôt dans un vault de yield sur Base.

### Vault : Aave V3 ou MockVault4626

**Aave V3 est déployé sur Base Sepolia** :

- Pool : `0x8bAB6d1b75f19e9eD9fCe8b9BD338844fF79aE27`
- USDC (Aave) : `0xba50Cd2A20f6DA35D788639E581bca8d0B5d4D5f`
- aUSDC : `0x10F1A9D11CDf50041f3f8cB7191CBE2f31750ACC`

**Attention :** l'USDC Aave (`0xba50...`) ≠ l'USDC Circle Gateway (`0x036C...`). À vérifier en étape 0 :

- Si compatible → utiliser `pool.supply(usdc, amount, onBehalfOf, 0)` (Aave V3 n'est pas ERC-4626 natif)
- Si incompatible → déployer un `MockVault4626` qui accepte le Circle USDC

### Interface — si Aave

```solidity
function mintAndSupply(
    bytes calldata attestationPayload,
    bytes calldata gatewaySignature
) external;
```

Flow : `gatewayMint` → mesurer delta USDC → `pool.supply(usdc, delta, shopAddress, 0)`

### Interface — si MockVault4626

```solidity
function mintAndDeposit(
    bytes calldata attestationPayload,
    bytes calldata gatewaySignature,
    uint256 minSharesOut
) external;
```

Flow : `gatewayMint` → mesurer delta USDC → `vault.deposit(delta, shopAddress)` → vérifier shares ≥ `minSharesOut`

### Allowance

`usdc.approve(pool_or_vault, type(uint256).max)` dans le constructor.

### Règles Gateway pour les intents de sweep

- `destinationRecipient = address(TreasuryComposer)`
- `destinationCaller = address(TreasuryComposer)` — restreint `msg.sender` du gatewayMint, empêche le front-running
- `hookData = 0x`

### Validation

```bash
# 1. Tests Foundry (avec MockVault4626)
forge test --match-contract TreasuryComposerTest -vvv
# attendu :
#   - USDC minté par gatewayMint arrive au TreasuryComposer
#   - delta USDC déposé dans le vault
#   - shares reçues > 0
#   - event TreasurySwept émis
#   - TreasuryComposer finit avec 0 USDC

# 2. Test d'allowance
cast call $COMPOSER "usdc()" --rpc-url $BASE_SEPOLIA_RPC
cast call $USDC "allowance(address,address)(uint256)" $COMPOSER $VAULT --rpc-url $BASE_SEPOLIA_RPC
# attendu : allowance == max uint256

# 3. Test de destinationCaller restriction
# tenter d'appeler gatewayMint depuis une adresse != TreasuryComposer
# avec un intent dont destinationCaller = TreasuryComposer
# attendu : revert

# 4. Si Aave : test d'intégration sur fork
forge test --fork-url $BASE_SEPOLIA_RPC --match-test testMintAndSupply -vvv
# attendu : pool.supply() réussit, aUSDC balance du shop augmente
```

---

## F7 — Sweep script

Déplacer un montant de la balance Gateway vers le vault sur Base.

### Flow

1. Lire les balances Gateway par domaine
2. Trier les domaines par coût
3. Allouer le montant à sweeper sur 1-2 chains
4. Construire les `TransferSpec`
5. Appeler `/v1/estimate` (fallback hardcodé si nécessaire)
6. Signer les burn intents
7. Appeler `/v1/transfer`
8. Appeler `TreasuryComposer.mintAndDeposit(...)` ou `mintAndSupply(...)` sur Base

### Règles

- Supporter 1-2 sources pour le POC
- Si la balance est fragmentée, utiliser plusieurs burn intents
- `minSharesOut = 0` pour la démo (MockVault seulement)
- Timeout 30 min sur les pollings
- **Pré-exécuter le sweep avant la démo** (30+ min sur testnets)

### Résultat attendu

`cmd/sweep/main.go --amount 100000000`

### Validation

```bash
# 1. Pré-requis : balance Gateway disponible sur au moins 1 chain
go run cmd/gateway-smoke-forwarder/main.go --balances-only
# attendu : balance > 0 sur au moins un domain

# 2. Sweep single-source
go run cmd/sweep/main.go --amount 1000000
# attendu :
#   - burn intent créé et signé
#   - /v1/transfer accepte l'intent
#   - attestation reçue (après attente)
#   - TreasuryComposer.mintAndDeposit tx success
#   - vault shares reçues > 0
#   - temps total loggé

# 3. Sweep multi-source (si balance fragmentée)
go run cmd/sweep/main.go --amount 5000000
# attendu : 2 burn intents créés si la balance est sur 2 chains

# 4. Vérifier le résultat on-chain
cast call $VAULT "balanceOf(address)(uint256)" $SHOP --rpc-url $BASE_SEPOLIA_RPC
# attendu : shares > 0
```

---

## F8 — Refund script

Rembourser un client en USDC sur la chain de son choix via le Forwarding Service.

### Flow

1. Lire les balances Gateway
2. Choisir une ou plusieurs sources
3. Construire les `TransferSpec` vers l'adresse client
4. Appeler `/v1/estimate?enableForwarder=true`
5. Signer
6. Appeler `/v1/transfer?enableForwarder=true`
7. Poller `GET /v1/transfer/{id}` jusqu'au succès (timeout 30 min)

### Résultat attendu

`cmd/refund/main.go --to 0xCLIENT --chain 10 --amount 5000000`

### Validation

```bash
# 1. Pré-requis : balance Gateway disponible
go run cmd/gateway-smoke-forwarder/main.go --balances-only

# 2. Refund cross-chain
go run cmd/refund/main.go --to 0xCLIENT --chain 10 --amount 1000000
# attendu :
#   - burn intent créé avec enableForwarder=true
#   - /v1/transfer accepte
#   - polling /v1/transfer/{id} → pending → confirmed
#   - balance USDC du client augmente sur chain 10
#   - temps total loggé

# 3. Vérifier la balance client on-chain
cast call $USDC "balanceOf(address)(uint256)" 0xCLIENT --rpc-url $UNICHAIN_SEPOLIA_RPC
# attendu : balance augmentée du montant - fee

# 4. Refund same-chain (si supporté)
go run cmd/refund/main.go --to 0xCLIENT --chain 6 --amount 500000
# attendu : même flow, potentiellement plus rapide
```

---

## Ordre d'implémentation

```
Étape 0 : Validation (quelques heures)
  ├── /v1/estimate sans enableForwarder → confirmer ou prévoir fallback hardcodé
  ├── chainId + verifyingContract du domain EIP-712 via /v1/info
  ├── Aave Base Sepolia accepte Circle USDC ? → décider Aave vs MockVault
  └── receiveWithAuthorization fonctionne sur Unichain Sepolia

Étape 1 : F1 — Gateway client Go (types, signer, HTTP)

Étape 2 : F2-B — Smoke test forwarding service

Étape 3 : F8 — Refund script (réutilise F2-B)

Étape 4 : F2-A — Smoke test self-managed mint

              ┌── Track Go ──────────────────┐
Étape 5 :     │  F4 — Relayer backend        │
              │  F5 — Payment watcher        │
              ├── Track Solidity ─────────────┤
              │  F3 — PaymentRouter           │
              │  F6 — TreasuryComposer + vault│
              └──────────────────────────────┘

Étape 6 : F7 — Sweep script (dépend de tout, pré-exécuter avant démo)
```

**Logique :** valider Gateway en premier (F1→F2-B→F8), puis le self-managed mint (F2-A), puis paralléliser Go backend et Solidity contracts, terminer par le sweep.

---

## Démo

1. Shop wallet pré-financé dans Gateway.
2. Refund cross-chain rapide (F8) — exécuté live.
3. Paiement client (F3+F4) — exécuté live.
4. `router_confirmed` → `gateway_available` — logs en live (ou pré-enregistrés si trop lent).
5. Sweep vers vault — montrer le résultat d'un sweep pré-exécuté.

---

## Structure du repo

```
go.mod
foundry.toml

internal/gateway/
  client.go              # F1
  types.go               # F1
  signer.go              # F1

cmd/
  gateway-smoke-selfmint/main.go   # F2-A
  gateway-smoke-forwarder/main.go  # F2-B
  refund/main.go                   # F8
  sweep/main.go                    # F7

contracts/
  PaymentRouter.sol                # F3
  TreasuryComposer.sol             # F6
  MockVault4626.sol                # F6 fallback
  test/
    PaymentRouter.t.sol
    TreasuryComposer.t.sol

internal/relayer/
  server.go              # F4
  nonce.go               # F4

internal/watcher/
  watcher.go             # F5
```

---

## Références

- Circle Gateway Contract Interfaces : https://developers.circle.com/gateway/references/contract-interfaces-and-events
- Circle Gateway Technical Guide : https://developers.circle.com/gateway/references/technical-guide
- Circle Gateway Forwarding Service : https://developers.circle.com/gateway/howtos/forwarding-service
- Circle Gateway Supported Blockchains : https://developers.circle.com/gateway/references/supported-blockchains
- CCTP Technical Guide : https://developers.circle.com/cctp/technical-guide
- circlefin/evm-gateway-contracts : https://github.com/circlefin/evm-gateway-contracts
- circlefin/stablecoin-evm : https://github.com/circlefin/stablecoin-evm
- Circle USDC Addresses : https://developers.circle.com/stablecoins/usdc-contract-addresses
- ERC-3009 : https://eips.ethereum.org/EIPS/eip-3009
- Aave V3 Base Sepolia : https://github.com/bgd-labs/aave-address-book/blob/main/src/AaveV3BaseSepolia.sol
- Aave V3 Docs : https://aave.com/docs/aave-v3/overview
- Uniswap Permit2 : https://docs.uniswap.org/contracts/permit2/overview
