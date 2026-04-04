// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test} from "forge-std/Test.sol";
import {ShopPaymaster, IERC20Permit, ITokenMessenger} from "../src/ShopPaymaster.sol";

contract ShopPaymasterTest is Test {
    ShopPaymaster paymaster;
    MockUSDC usdc;
    MockTokenMessenger messenger;

    address customer;
    uint256 customerKey;

    address gatewayWallet = address(0x0077777d7EBA4688BDeF3E311b846F25870A19B9);
    uint32 arcDomain = 26;

    function setUp() public {
        (customer, customerKey) = makeAddrAndKey("customer");

        usdc = new MockUSDC();
        messenger = new MockTokenMessenger();

        paymaster = new ShopPaymaster(
            address(usdc),
            address(messenger),
            arcDomain,
            gatewayWallet
        );

        // Fund customer with 100 USDC.
        usdc.mint(customer, 100e6);
    }

    function test_payWithPermit() public {
        uint256 amount = 10e6; // 10 USDC
        uint256 deadline = block.timestamp + 1 hours;

        // Sign permit.
        (uint8 v, bytes32 r, bytes32 s) = _signPermit(customer, customerKey, address(paymaster), amount, deadline);

        // Execute payment.
        paymaster.payWithPermit(customer, amount, deadline, v, r, s, 500_000);

        // Customer debited.
        assertEq(usdc.balanceOf(customer), 90e6);

        // Paymaster has 0 USDC (all bridged).
        assertEq(usdc.balanceOf(address(paymaster)), 0);

        // TokenMessenger received the call.
        assertEq(messenger.lastAmount(), amount);
        assertEq(messenger.lastDestDomain(), arcDomain);
        assertEq(messenger.lastMintRecipient(), bytes32(uint256(uint160(gatewayWallet))));
        assertEq(messenger.lastBurnToken(), address(usdc));
    }

    function test_paymentEvent() public {
        uint256 amount = 5e6;
        uint256 deadline = block.timestamp + 1 hours;
        (uint8 v, bytes32 r, bytes32 s) = _signPermit(customer, customerKey, address(paymaster), amount, deadline);

        vm.expectEmit(true, false, false, true);
        emit ShopPaymaster.Payment(customer, amount);
        paymaster.payWithPermit(customer, amount, deadline, v, r, s, 500_000);
    }

    function test_backendCanRelay() public {
        uint256 amount = 10e6;
        uint256 deadline = block.timestamp + 1 hours;
        (uint8 v, bytes32 r, bytes32 s) = _signPermit(customer, customerKey, address(paymaster), amount, deadline);

        // msg.sender = address(this) (the test contract, acting as backend).
        // Not the customer — proves the relay pattern works.
        paymaster.payWithPermit(customer, amount, deadline, v, r, s, 500_000);

        assertEq(usdc.balanceOf(customer), 90e6);
        assertEq(usdc.balanceOf(address(paymaster)), 0);
    }

    function test_revert_expiredPermit() public {
        uint256 amount = 10e6;
        uint256 deadline = block.timestamp - 1; // expired

        (uint8 v, bytes32 r, bytes32 s) = _signPermit(customer, customerKey, address(paymaster), amount, deadline);

        vm.expectRevert("permit expired");
        paymaster.payWithPermit(customer, amount, deadline, v, r, s, 500_000);
    }

    function test_revert_insufficientBalance() public {
        uint256 amount = 200e6; // more than customer has
        uint256 deadline = block.timestamp + 1 hours;
        (uint8 v, bytes32 r, bytes32 s) = _signPermit(customer, customerKey, address(paymaster), amount, deadline);

        vm.expectRevert("insufficient balance");
        paymaster.payWithPermit(customer, amount, deadline, v, r, s, 500_000);
    }

    function test_constructorParams() public view {
        assertEq(address(paymaster.usdc()), address(usdc));
        assertEq(address(paymaster.tokenMessenger()), address(messenger));
        assertEq(paymaster.arcDomain(), arcDomain);
        assertEq(paymaster.mintRecipient(), bytes32(uint256(uint160(gatewayWallet))));
    }

    // -----------------------------------------------------------------------
    // EIP-2612 permit signing helper
    // -----------------------------------------------------------------------

    function _signPermit(
        address owner,
        uint256 ownerKey,
        address spender,
        uint256 value,
        uint256 deadline
    ) internal view returns (uint8 v, bytes32 r, bytes32 s) {
        bytes32 structHash = keccak256(abi.encode(
            keccak256("Permit(address owner,address spender,uint256 value,uint256 nonce,uint256 deadline)"),
            owner,
            spender,
            value,
            usdc.nonces(owner),
            deadline
        ));
        bytes32 digest = keccak256(abi.encodePacked(
            "\x19\x01",
            usdc.DOMAIN_SEPARATOR(),
            structHash
        ));
        (v, r, s) = vm.sign(ownerKey, digest);
    }
}

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

/// @dev Minimal mock USDC with EIP-2612 permit support.
contract MockUSDC {
    string public constant name = "USDC";
    string public constant version = "2";
    uint8 public constant decimals = 6;

    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;
    mapping(address => uint256) public nonces;

    bytes32 public DOMAIN_SEPARATOR;

    constructor() {
        DOMAIN_SEPARATOR = keccak256(abi.encode(
            keccak256("EIP712Domain(string name,string version,uint256 chainId,address verifyingContract)"),
            keccak256(bytes(name)),
            keccak256(bytes(version)),
            block.chainid,
            address(this)
        ));
    }

    function mint(address to, uint256 amount) external {
        balanceOf[to] += amount;
    }

    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
        return true;
    }

    function transfer(address to, uint256 amount) external returns (bool) {
        require(balanceOf[msg.sender] >= amount, "insufficient balance");
        balanceOf[msg.sender] -= amount;
        balanceOf[to] += amount;
        return true;
    }

    function transferFrom(address from, address to, uint256 amount) external returns (bool) {
        require(balanceOf[from] >= amount, "insufficient balance");
        require(allowance[from][msg.sender] >= amount, "insufficient allowance");
        allowance[from][msg.sender] -= amount;
        balanceOf[from] -= amount;
        balanceOf[to] += amount;
        return true;
    }

    function permit(
        address owner,
        address spender,
        uint256 value,
        uint256 deadline,
        uint8 v,
        bytes32 r,
        bytes32 s
    ) external {
        require(deadline >= block.timestamp, "permit expired");

        bytes32 structHash = keccak256(abi.encode(
            keccak256("Permit(address owner,address spender,uint256 value,uint256 nonce,uint256 deadline)"),
            owner,
            spender,
            value,
            nonces[owner]++,
            deadline
        ));
        bytes32 digest = keccak256(abi.encodePacked(
            "\x19\x01",
            DOMAIN_SEPARATOR,
            structHash
        ));
        address recovered = ecrecover(digest, v, r, s);
        require(recovered == owner, "invalid permit signature");

        allowance[owner][spender] = value;
    }
}

/// @dev Mock TokenMessenger that records the last call.
contract MockTokenMessenger {
    uint256 public lastAmount;
    uint32 public lastDestDomain;
    bytes32 public lastMintRecipient;
    address public lastBurnToken;
    uint256 public lastMaxFee;

    function depositForBurn(
        uint256 amount,
        uint32 destinationDomain,
        bytes32 _mintRecipient,
        address burnToken,
        bytes32, // destinationCaller
        uint256 maxFee,
        uint32  // minFinalityThreshold
    ) external {
        // Pull tokens from caller (like the real contract does).
        IERC20Permit(burnToken).transferFrom(msg.sender, address(this), amount);

        lastAmount = amount;
        lastDestDomain = destinationDomain;
        lastMintRecipient = _mintRecipient;
        lastBurnToken = burnToken;
        lastMaxFee = maxFee;
    }
}
