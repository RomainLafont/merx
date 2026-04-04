# Spec : boutique on-chain avec Circle Gateway, paiement gasless, yield et refund cross-chain

## Contexte

POC de boutique on-chain dont la trésorerie est centrée sur Circle Gateway :

1. Le client paie en USDC depuis une chain supportée — le backend prépare une tx `depositForBurnWithHook` que le client signe et exécute.
2. Le CCTP V2 Forwarding Service bridge l'USDC vers Arc (domain 26) et le dépose automatiquement dans Gateway en une seule tx.
3. La boutique accumule une balance Gateway unifiée.
4. Une partie de cette balance est sweep vers un vault de yield sur Base.
5. La boutique peut rembourser instantanément un client sur une autre chain.

---

## Décisions structurantes

### D1. Paiement entrant : CCTP V2 Forwarding Service vers Arc

Le client exécute une seule tx `depositForBurnWithHook` sur la source chain. Le Forwarding Service de Circle bridge l'USDC vers Arc (domain 26) et dépose automatiquement dans Gateway — pas de `depositFor` ni de contrat intermédiaire côté source.

Pourquoi :

- Une seule tx côté client (pas de relayer, pas de PaymentRouter)
- Le Forwarding Service gère le mint + dépôt sur Arc automatiquement
- Pas besoin de gas ni de wallet côté destination
- Le backend prépare la tx non signée via `GET /api/pay-tx`, le client la signe et l'exécute lui-même

### D2. Gateway : primitives réelles

Le client Gateway est construit autour des vraies primitives Circle :

- `TransferSpec` (14 champs, types vérifiés)
- `BurnIntent` (3 champs)
- `/v1/estimate` pour `maxFee` et `maxBlockHeight`
- CCTP V2 `depositForBurnWithHook` pour le bridge source → Arc + dépôt Gateway

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

1. Succès on-chain du `depositForBurnWithHook` sur la source chain → event CCTP `DepositForBurn`
2. Disponibilité réelle dans Gateway via `/v1/deposits` puis `/v1/balances` sur Arc

Un event `DepositForBurn` sur la source ne prouve pas que la balance Gateway est spendable — il faut attendre le mint sur Arc.

---

## Contraintes réseau

Chains de démo : **Base Sepolia** (domain 6) et **Unichain Sepolia** (domain 10).

Finalité Gateway ~20 min sur ces testnets. Non bloquant pour la démo : les étapes longues seront démontrées avec des calls pré-exécutés.

---

## Architecture

```
CLIENT (wallet, source chain)
  │
  │ GET /api/pay-tx → reçoit tx non signée
  │ signe et exécute depositForBurnWithHook(...)
  ▼
TokenMessengerV2 (source chain)
  │ burn USDC + hookData = "cctp-forward"
  │ destinationDomain = 26 (Arc)
  │ mintRecipient = Gateway Wallet sur Arc
  ▼
Circle Forwarding Service
  │ atteste le burn, mint automatiquement sur Arc
  │ dépose dans Gateway Wallet
  ▼
Gateway offchain ledger (balance unifiée sur Arc)
  │
  ├──► F5 watcher
  │      poll /v1/deposits + /v1/balances
  │      log: cctp_burned → gateway_available
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
F3: Pay-tx endpoint (prépare la tx depositForBurnWithHook pour le client)
  └── depends on F1
F5: Payment watcher
  └── depends on F3
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

## F3 — Pay-tx endpoint

Préparer une transaction `depositForBurnWithHook` non signée que le client récupère, signe et exécute lui-même. Plus besoin de PaymentRouter ni de relayer — le client interagit directement avec le TokenMessengerV2 de CCTP.

### Principe

Le backend expose un endpoint `GET /api/pay-tx` qui retourne les données nécessaires pour construire la tx côté client (calldata, adresse cible, chain info). Le client doit au préalable avoir approuvé le TokenMessengerV2 pour le montant USDC.

### Adresses

| Chain | Domain | USDC | TokenMessengerV2 |
|-------|--------|------|------------------|
| Unichain Sepolia | 10 | `0x31d0220469e10c4E71834a79b1f276d740d3768F` | `0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA` |
| Base Sepolia | 6 | `0x036CbD53842c5426634e7929541eC2318f3dCF7e` | `0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA` |
| Ethereum Sepolia | 0 | `0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238` | `0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA` |

- **Arc Testnet** : domain 26
- **Gateway Wallet** : `0x0077777d7EBA4688BDeF3E311b846F25870A19B9` (mintRecipient sur Arc)

### Fonction CCTP V2 appelée par le client

```solidity
function depositForBurnWithHook(
    uint256 amount,
    uint32 destinationDomain,       // 26 (Arc)
    bytes32 mintRecipient,          // Gateway Wallet sur Arc, left-padded
    address burnToken,              // USDC sur source chain
    bytes32 destinationCaller,      // 0x0 (permissionless, forwarding service)
    uint256 maxFee,                 // couvre protocol fee + forwarding fee
    uint32 minFinalityThreshold,    // 0 pour fast transfer
    bytes calldata hookData         // magic bytes "cctp-forward"
) external;
```

### hookData pour le Forwarding Service

Format minimal (32 bytes) :
```
0x636374702d666f72776172640000000000000000000000000000000000000000
```

Layout :
- bytes 0-23 : magic `"cctp-forward"`
- bytes 24-27 : version = `0`
- bytes 28-31 : circle data length = `0`

### Fees du Forwarding Service

| Destination | Service Fee |
|-------------|------------|
| Ethereum | $1.25 USDC |
| Autres chains | $0.20 USDC |

`maxFee` doit couvrir protocol fee + forwarding fee.

### Endpoint API

```
GET /api/pay-tx?chain_id=1301&amount=1000000
```

Réponse :

```json
{
  "to": "0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA",
  "data": "0x...",
  "chain_id": 1301,
  "value": "0",
  "token": "0x31d0220469e10c4E71834a79b1f276d740d3768F",
  "approval_needed": {
    "spender": "0x8FE6B999Dc680CcFDD5Bf7EB0974218be2542DAA",
    "amount": "1000000"
  }
}
```

Le champ `approval_needed` indique que le client doit d'abord faire un `approve(TokenMessengerV2, amount)` sur le contrat USDC si ce n'est pas déjà fait.

### Flow complet côté client

1. `GET /api/pay-tx?chain_id=1301&amount=1000000`
2. Si `approval_needed` → signer et exécuter `usdc.approve(spender, amount)`
3. Signer et exécuter la tx `depositForBurnWithHook` avec les données retournées
4. Le Forwarding Service bridge vers Arc et dépose dans Gateway automatiquement

### Implémentation backend

1. Résoudre la source chain (chain_id → domain, USDC address, TokenMessengerV2)
2. Construire le calldata `depositForBurnWithHook` :
   - `amount` = montant demandé
   - `destinationDomain` = 26 (Arc)
   - `mintRecipient` = Gateway Wallet paddé en bytes32
   - `burnToken` = USDC sur source chain
   - `destinationCaller` = bytes32(0)
   - `maxFee` = estimation via fee constants
   - `minFinalityThreshold` = 0
   - `hookData` = `0x636374702d666f727761726400...` (32 bytes)
3. Retourner la tx non signée

### Validation

```bash
# 1. Démarrer le serveur
PRIVATE_KEY=0x... go run cmd/server/main.go --port 8080

# 2. Récupérer une tx préparée
curl "http://localhost:8080/api/pay-tx?chain_id=1301&amount=1000000"
# attendu : JSON avec to, data, chain_id, approval_needed

# 3. Vérifier le calldata
# décoder le data avec cast pour vérifier les paramètres :
cast calldata-decode "depositForBurnWithHook(uint256,uint32,bytes32,address,bytes32,uint256,uint32,bytes)" $DATA
# attendu :
#   amount = 1000000
#   destinationDomain = 26
#   mintRecipient = 0x...0077777d7EBA4688BDeF3E311b846F25870A19B9
#   burnToken = USDC address de la chain
#   destinationCaller = 0x0
#   hookData commence par 0x636374702d666f7277617264

# 4. Test d'intégration (avec wallet funded sur testnet)
# Exécuter la tx retournée depuis un wallet avec USDC
# Vérifier que le Forwarding Service mint sur Arc
# Vérifier /v1/balances augmente
```

---

~~F4 — Relayer backend~~ *Supprimé* : plus nécessaire. Le client exécute lui-même la tx `depositForBurnWithHook` — pas besoin de relayer ni de nonce management côté backend.

---

## F5 — Payment watcher

Suivre le paiement de bout en bout : burn CCTP sur la source chain puis disponibilité effective dans Gateway sur Arc.

### Comportement

1. Écouter l'event `DepositForBurn` du TokenMessengerV2 sur chaque source chain (filtrer par `mintRecipient` = Gateway Wallet)
2. Corréler avec le montant et l'adresse du sender
3. Poller `/v1/deposits` et `/v1/balances` sur Gateway
4. Logger deux états : `cctp_burned` → `gateway_available`

### Corrélation

Si `/v1/deposits` expose le tx hash source → corrélation directe. Sinon → heuristique montant + timestamp + source domain.

### Persistence

- Persister `lastScannedBlock` par chain dans un fichier JSON
- Permet un redémarrage propre sans rescanner depuis le genesis
- Polling HTTP toutes les 5 secondes

### Validation

```bash
# 1. Démarrer le watcher
go run cmd/watcher/main.go

# 2. Déclencher un paiement via F3 (pay-tx endpoint + exécution client)
# attendu dans les logs du watcher :
#   [cctp_burned] from=0xCLIENT chain=1301 tx=0x... amount=1000000
#   ... (attendre finalité CCTP + Forwarding Service) ...
#   [gateway_available] balance_delta=+1000000

# 3. Tester le restart
# arrêter le watcher, vérifier que state.json contient lastScannedBlock
cat state.json
# attendu : {"1301": 12345, "84532": ...}
# redémarrer le watcher → doit reprendre au bon block, pas d'events dupliqués
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
  └── Aave Base Sepolia accepte Circle USDC ? → décider Aave vs MockVault

Étape 1 : F1 — Gateway client Go (types, signer, HTTP) ✅

Étape 2 : F2 — Gateway smoke tests ✅

Étape 3 : F8 — Refund script + API server ✅

Étape 4 : F3 — Pay-tx endpoint (depositForBurnWithHook)

              ┌── Track Go ──────────────────┐
Étape 5 :     │  F5 — Payment watcher        │
              ├── Track Solidity ─────────────┤
              │  F6 — TreasuryComposer + vault│
              └──────────────────────────────┘

Étape 6 : F7 — Sweep script (dépend de tout, pré-exécuter avant démo)
```

**Logique :** F1→F2→F8 sont faits. Prochaine étape : F3 (pay-tx endpoint) pour le flow de paiement client. Puis paralléliser watcher Go et TreasuryComposer Solidity, terminer par le sweep.

---

## Démo

1. Shop wallet pré-financé dans Gateway.
2. Refund cross-chain rapide (F8) — exécuté live.
3. Paiement client : `GET /api/pay-tx` → client signe → `depositForBurnWithHook` → USDC arrive sur Arc dans Gateway.
4. `cctp_burned` → `gateway_available` — logs en live (ou pré-enregistrés si trop lent).
5. Sweep vers vault — montrer le résultat d'un sweep pré-exécuté.

---

## Structure du repo

```
go.mod

gateway/
  client.go              # F1
  types.go               # F1
  signer.go              # F1
  allocate.go            # F1

cmd/
  server/main.go         # F3 (pay-tx endpoint) + F8 (refund endpoints)
  refund/main.go         # F8 (CLI)
  sweep/main.go          # F7

contracts/
  TreasuryComposer.sol   # F6
  MockVault4626.sol      # F6 fallback
  test/
    TreasuryComposer.t.sol

cmd/watcher/main.go      # F5
```

---

## Références

- Circle Gateway Contract Interfaces : https://developers.circle.com/gateway/references/contract-interfaces-and-events
- Circle Gateway Technical Guide : https://developers.circle.com/gateway/references/technical-guide
- Circle Gateway Forwarding Service : https://developers.circle.com/gateway/howtos/forwarding-service
- Circle Gateway Supported Blockchains : https://developers.circle.com/gateway/references/supported-blockchains
- CCTP V2 Technical Guide : https://developers.circle.com/cctp/technical-guide
- CCTP V2 Forwarding Service : https://developers.circle.com/cctp/concepts/forwarding-service
- CCTP V2 Supported Chains & Domains : https://developers.circle.com/cctp/concepts/supported-chains-and-domains
- circlefin/evm-gateway-contracts : https://github.com/circlefin/evm-gateway-contracts
- circlefin/stablecoin-evm : https://github.com/circlefin/stablecoin-evm
- Circle USDC Addresses : https://developers.circle.com/stablecoins/usdc-contract-addresses
- Aave V3 Base Sepolia : https://github.com/bgd-labs/aave-address-book/blob/main/src/AaveV3BaseSepolia.sol
- Aave V3 Docs : https://aave.com/docs/aave-v3/overview
