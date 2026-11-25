"""Portabel wallet, Jupiter Swap builder, dan Solana RPC client.

Desain bertahap (portable):
- Tahap 1 (--mode live, PAPER=True): Tidak memakai private key; hanya quote read-only.
- Tahap 2 (Dry-run / simulateTransaction): Memakai JSON-RPC stdlib + Jupiter Quote/Swap
  untuk menguji simulasi transaksi on-chain TANPA menandatangani atau mengirim uang asli.
- Tahap 3 (Live on-chain): Lazy-import `solders` untuk load Keypair dari ENV
  (`SOLANA_PRIVATE_KEY`), sign VersionedTransaction, preflight simulateTransaction,
  sendTransaction, lalu poll konfirmasi sampai `confirmed`/`finalized`.
"""

from __future__ import annotations

import base64
import json
import os
import time
from pathlib import Path
from typing import Any
from urllib.parse import urlencode

import config
from agents import fetch_json, post_json


class WalletError(Exception):
    """Error pada RPC Solana, pembuatan swap Jupiter, atau signing transaksi."""


# ---------------------------------------------------------------------------
# 1. Solana JSON-RPC helper (100% stdlib, tanpa dependensi eksternal)
# ---------------------------------------------------------------------------

def rpc_call(method: str, params: list[Any], rpc_url: str | None = None, timeout: float = 12.0) -> Any:
    """Kirim request JSON-RPC 2.0 ke endpoint Solana RPC."""
    url = (rpc_url or config.SOLANA_RPC_URL).strip()
    if not url:
        raise WalletError("SOLANA_RPC_URL belum diset")
    payload = {"jsonrpc": "2.0", "id": 1, "method": method, "params": params}
    res = post_json(url, payload, timeout=timeout)
    if not isinstance(res, dict):
        raise WalletError(f"RPC {method}: respons tidak valid")
    if res.get("error"):
        raise WalletError(f"RPC {method} error: {res['error']}")
    return res.get("result")


def get_sol_balance(owner_pubkey: str, rpc_url: str | None = None) -> float:
    """Ambil saldo native SOL (dalam satuan SOL, untuk cek biaya gas/rent)."""
    res = rpc_call("getBalance", [owner_pubkey, {"commitment": "confirmed"}], rpc_url=rpc_url)
    lamports = int((res or {}).get("value") or 0)
    return lamports / 1_000_000_000.0


def get_token_balance_raw(owner_pubkey: str, mint: str, rpc_url: str | None = None) -> tuple[int, int]:
    """Ambil saldo SPL token (amount_raw, decimals) milik wallet untuk mint tertentu."""
    res = rpc_call(
        "getTokenAccountsByOwner",
        [
            owner_pubkey,
            {"mint": mint},
            {"encoding": "jsonParsed", "commitment": "confirmed"},
        ],
        rpc_url=rpc_url,
    )
    total_raw = 0
    decimals = 6
    for acc in (res or {}).get("value") or []:
        info = (((acc.get("account") or {}).get("data") or {}).get("parsed") or {}).get("info") or {}
        tok_amt = info.get("tokenAmount") or {}
        total_raw += int(tok_amt.get("amount") or 0)
        if tok_amt.get("decimals") is not None:
            decimals = int(tok_amt["decimals"])
    return total_raw, decimals


def get_usdc_balance(owner_pubkey: str, rpc_url: str | None = None) -> float:
    """Ambil saldo USDC wallet dalam satuan USD."""
    raw, dec = get_token_balance_raw(owner_pubkey, config.USDC_MINT, rpc_url=rpc_url)
    return raw / (10 ** dec)


# ---------------------------------------------------------------------------
# 2. Jupiter Quote & Swap transaction builder (stdlib HTTPS)
# ---------------------------------------------------------------------------

def jupiter_quote(
    input_mint: str,
    output_mint: str,
    amount_raw: int,
    slippage_bps: int | None = None,
) -> dict[str, Any]:
    """Ambil full quote object dari Jupiter v1 API."""
    if amount_raw <= 0:
        raise WalletError("amount_raw harus > 0")
    qs = urlencode({
        "inputMint": input_mint,
        "outputMint": output_mint,
        "amount": str(int(amount_raw)),
        "slippageBps": str(int(slippage_bps if slippage_bps is not None else config.SLIPPAGE_BPS)),
    })
    url = f"{config.JUPITER_QUOTE_URL}?{qs}"
    quote = fetch_json(url, timeout=10.0)
    if not isinstance(quote, dict) or not quote.get("outAmount"):
        raise WalletError("Jupiter quote kosong atau gagal")
    return quote


def jupiter_build_swap_tx(
    quote_response: dict[str, Any],
    user_pubkey: str,
    priority_fee_lamports: int | None = None,
) -> str:
    """Minta transaksi base64 (unsigned VersionedTransaction) dari Jupiter /swap."""
    fee = int(priority_fee_lamports if priority_fee_lamports is not None else config.PRIORITY_FEE_LAMPORTS)
    payload = {
        "quoteResponse": quote_response,
        "userPublicKey": user_pubkey,
        "wrapAndUnwrapSol": True,
        "dynamicComputeUnitLimit": True,
        "prioritizationFeeLamports": fee,
    }
    res = post_json(config.JUPITER_SWAP_URL, payload, timeout=15.0)
    tx_b64 = (res or {}).get("swapTransaction")
    if not tx_b64 or not isinstance(tx_b64, str):
        raise WalletError(f"Jupiter swap tidak mengembalikan swapTransaction: {res}")
    return tx_b64


def simulate_transaction(
    tx_base64: str,
    *,
    sig_verify: bool = False,
    rpc_url: str | None = None,
) -> dict[str, Any]:
    """Jalankan Solana RPC `simulateTransaction` sebagai dry-run/preflight.

    Catatan:
    - Tahap 2 (dry-run tanpa key): `sig_verify=False` untuk cek apakah instruksi swap valid.
    - Tahap 3 (preflight sebelum kirim): `sig_verify=True` setelah ditandatangani keypair.
    """
    res = rpc_call(
        "simulateTransaction",
        [
            tx_base64,
            {
                "encoding": "base64",
                "sigVerify": sig_verify,
                "replaceRecentBlockhash": not sig_verify,
                "commitment": "confirmed",
            },
        ],
        rpc_url=rpc_url,
    )
    val = (res or {}).get("value") or {}
    return {
        "ok": val.get("err") is None,
        "err": val.get("err"),
        "units_consumed": val.get("unitsConsumed"),
        "logs": (val.get("logs") or [])[-8:],
    }


def dry_run_swap_plan(
    input_mint: str,
    output_mint: str,
    amount_raw: int,
    owner_pubkey: str,
    rpc_url: str | None = None,
) -> dict[str, Any]:
    """Tahap 2 helper: uji Jupiter quote + build swap tx + RPC simulateTransaction.

    Aman dijalankan tanpa private key karena tidak menandatangani atau mengirim transaksi.
    """
    quote = jupiter_quote(input_mint, output_mint, amount_raw)
    tx_b64 = jupiter_build_swap_tx(quote, owner_pubkey)
    sim = simulate_transaction(tx_b64, sig_verify=False, rpc_url=rpc_url)
    return {
        "ok": bool(sim["ok"]),
        "in_amount_raw": int(quote.get("inAmount") or amount_raw),
        "out_amount_raw": int(quote.get("outAmount") or 0),
        "price_impact_pct": float(quote.get("priceImpactPct") or 0.0),
        "simulation": sim,
    }


# ---------------------------------------------------------------------------
# 3. Stage 3 Keypair & Ed25519 Signing (lazy-import `solders`)
# ---------------------------------------------------------------------------

def has_solders() -> bool:
    """Cek apakah paket `solders` terpasang untuk signing live on-chain."""
    try:
        import solders  # noqa: F401
        return True
    except ImportError:
        return False


def _require_solders() -> tuple[Any, Any]:
    try:
        from solders.keypair import Keypair  # type: ignore
        from solders.transaction import VersionedTransaction  # type: ignore
        return Keypair, VersionedTransaction
    except ImportError as exc:
        raise WalletError(
            "Paket 'solders' belum terpasang. Jalankan: pip install -r desk/requirements-live.txt"
        ) from exc


def load_keypair() -> Any:
    """Muat Solana Keypair dari ENV `SOLANA_PRIVATE_KEY` (base58, JSON array, atau path file)."""
    Keypair, _ = _require_solders()
    raw = os.environ.get("SOLANA_PRIVATE_KEY", "").strip()
    if not raw:
        raise WalletError("ENV SOLANA_PRIVATE_KEY belum diset")

    # Dukung path menuju file keypair JSON (misal ~/.config/solana/burner.json)
    p = Path(raw).expanduser()
    if p.exists() and p.is_file():
        raw = p.read_text(encoding="utf-8").strip()

    if raw.startswith("["):
        arr = json.loads(raw)
        return Keypair.from_bytes(bytes(arr))
    return Keypair.from_base58_string(raw)


def wallet_pubkey() -> str:
    """Ambil public key (base58) dari `SOLANA_PUBLIC_KEY` atau `SOLANA_PRIVATE_KEY`."""
    pub = os.environ.get("SOLANA_PUBLIC_KEY", "").strip()
    if pub:
        return pub
    kp = load_keypair()
    return str(kp.pubkey())


def sign_swap_tx(swap_tx_b64: str, keypair: Any) -> str:
    """Dekode transaksi base64 dari Jupiter, tandatangani dengan Keypair, kembalikan base64."""
    _, VersionedTransaction = _require_solders()
    raw_bytes = base64.b64decode(swap_tx_b64)
    unsigned_tx = VersionedTransaction.from_bytes(raw_bytes)
    signed_tx = VersionedTransaction(unsigned_tx.message, [keypair])
    return base64.b64encode(bytes(signed_tx)).decode("ascii")


def send_and_confirm(
    signed_tx_b64: str,
    rpc_url: str | None = None,
    timeout_sec: float | None = None,
) -> str:
    """Preflight simulate -> sendTransaction -> poll konfirmasi hingga confirmed/finalized."""
    sim = simulate_transaction(signed_tx_b64, sig_verify=True, rpc_url=rpc_url)
    if not sim["ok"]:
        raise WalletError(f"Preflight simulation gagal: {sim['err']}")

    sig = rpc_call(
        "sendTransaction",
        [
            signed_tx_b64,
            {"encoding": "base64", "skipPreflight": False, "preflightCommitment": "confirmed", "maxRetries": 3},
        ],
        rpc_url=rpc_url,
    )
    if not sig or not isinstance(sig, str):
        raise WalletError("sendTransaction tidak mengembalikan signature")

    deadline = time.time() + float(timeout_sec or config.TX_CONFIRM_TIMEOUT_SEC)
    while time.time() < deadline:
        st = rpc_call("getSignatureStatuses", [[sig], {"searchTransactionHistory": True}], rpc_url=rpc_url)
        statuses = (st or {}).get("value") or []
        info = statuses[0] if statuses else None
        if isinstance(info, dict):
            if info.get("err") is not None:
                raise WalletError(f"Transaksi gagal on-chain ({sig}): {info['err']}")
            if info.get("confirmationStatus") in ("confirmed", "finalized"):
                return sig
        time.sleep(1.5)

    raise WalletError(f"Timeout menunggu konfirmasi tx {sig}")


def execute_swap(
    input_mint: str,
    output_mint: str,
    amount_raw: int,
    *,
    dry_run: bool = False,
    slippage_bps: int | None = None,
    rpc_url: str | None = None,
) -> dict[str, Any]:
    """Eksekusi swap penuh (Jupiter Quote -> Swap -> Sign -> Simulate -> Send -> Confirm)."""
    try:
        if dry_run:
            pub = wallet_pubkey()
            res = dry_run_swap_plan(input_mint, output_mint, amount_raw, pub, rpc_url=rpc_url)
            res["tx_sig"] = None
            res["dry_run"] = True
            return res

        kp = load_keypair()
        pub = str(kp.pubkey())
        quote = jupiter_quote(input_mint, output_mint, amount_raw, slippage_bps=slippage_bps)
        tx_b64 = jupiter_build_swap_tx(quote, pub)
        signed_b64 = sign_swap_tx(tx_b64, kp)
        tx_sig = send_and_confirm(signed_b64, rpc_url=rpc_url)
        return {
            "ok": True,
            "dry_run": False,
            "tx_sig": tx_sig,
            "in_amount_raw": int(quote.get("inAmount") or amount_raw),
            "out_amount_raw": int(quote.get("outAmount") or 0),
            "price_impact_pct": float(quote.get("priceImpactPct") or 0.0),
            "err": None,
        }
    except Exception as exc:
        return {
            "ok": False,
            "dry_run": dry_run,
            "tx_sig": None,
            "in_amount_raw": amount_raw,
            "out_amount_raw": 0,
            "price_impact_pct": 0.0,
            "err": f"{type(exc).__name__}: {exc}",
        }
