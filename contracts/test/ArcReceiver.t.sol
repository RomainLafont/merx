// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Test} from "forge-std/Test.sol";
import {ArcReceiver} from "../src/ArcReceiver.sol";

contract ArcReceiverTest is Test {
    ArcReceiver receiver;
    MockUSDC usdc;
    MockGatewayWallet gateway;
    MockMessageTransmitter transmitter;
    address operator;
    address shop = address(0x5408);

    function setUp() public {
        operator = address(this);
        usdc = new MockUSDC();
        gateway = new MockGatewayWallet();
        transmitter = new MockMessageTransmitter(address(usdc));
        receiver = new ArcReceiver(
            address(usdc),
            address(gateway),
            operator,
            address(transmitter)
        );
        // Tell the mock transmitter to mint to the receiver.
        transmitter.setMintTarget(address(receiver));
    }

    function test_relayAndDeposit() public {
        // The mock transmitter will mint 1 USDC to the receiver on receiveMessage.
        transmitter.setMintAmount(1e6);

        receiver.relayAndDeposit("message", "attestation", shop);

        // Receiver has 0 USDC after (all deposited).
        assertEq(usdc.balanceOf(address(receiver)), 0);

        // Gateway received the depositFor call.
        assertEq(gateway.lastToken(), address(usdc));
        assertEq(gateway.lastDepositor(), shop);
        assertEq(gateway.lastValue(), 1e6);
    }

    function test_event() public {
        transmitter.setMintAmount(5e6);

        vm.expectEmit(true, false, false, true);
        emit ArcReceiver.Deposited(shop, 5e6);
        receiver.relayAndDeposit("msg", "att", shop);
    }

    function test_revert_notOperator() public {
        transmitter.setMintAmount(1e6);

        vm.prank(address(0xBAD));
        vm.expectRevert("only operator");
        receiver.relayAndDeposit("msg", "att", shop);
    }

    function test_revert_receiveMessageFails() public {
        transmitter.setShouldFail(true);

        vm.expectRevert("receiveMessage failed");
        receiver.relayAndDeposit("msg", "att", shop);
    }

    function test_constructorParams() public view {
        assertEq(address(receiver.usdc()), address(usdc));
        assertEq(receiver.gatewayWallet(), address(gateway));
        assertEq(receiver.operator(), operator);
        assertEq(address(receiver.messageTransmitter()), address(transmitter));
    }
}

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

contract MockUSDC {
    mapping(address => uint256) public balanceOf;
    mapping(address => mapping(address => uint256)) public allowance;

    function mint(address to, uint256 amount) external {
        balanceOf[to] += amount;
    }

    function approve(address spender, uint256 amount) external returns (bool) {
        allowance[msg.sender][spender] = amount;
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
}

contract MockGatewayWallet {
    address public lastToken;
    address public lastDepositor;
    uint256 public lastValue;

    function depositFor(address token, address depositor_, uint256 value) external {
        MockUSDC(token).transferFrom(msg.sender, address(this), value);
        lastToken = token;
        lastDepositor = depositor_;
        lastValue = value;
    }
}

contract MockMessageTransmitter {
    MockUSDC immutable usdc;
    address mintTarget;
    uint256 mintAmount;
    bool shouldFail;

    constructor(address _usdc) {
        usdc = MockUSDC(_usdc);
    }

    function setMintTarget(address target) external { mintTarget = target; }
    function setMintAmount(uint256 amount) external { mintAmount = amount; }
    function setShouldFail(bool fail) external { shouldFail = fail; }

    function receiveMessage(bytes calldata, bytes calldata) external returns (bool) {
        if (shouldFail) return false;
        usdc.mint(mintTarget, mintAmount);
        return true;
    }
}
