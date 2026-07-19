# OANDA Australia — account / instrument constraints

Source: Client Experience email from **Ken Leander** (OANDA), addressed to **y m**,
filed for Tradex planning. Not a legal document — verify against current OANDA AU
help pages before go-live.

## Summary (what matters for Tradex)

| Product | Where (AU division, per email) |
| --- | --- |
| **Index CFDs** | Available in region (with forex, metals, commodities, bonds, crypto) |
| **Share CFDs** | **OANDA One** sub-account only (MT5 / OANDA mobile / TradingView) |
| **ETF CFDs** | **Not offered** in Australia |
| **MT4** | Does **not** support Share CFDs |

**Tradex EU LOVE** targets **index CFDs** (`DE30_EUR` / `FR40_EUR` / optionally
`EU50_EUR` on practice v20 API — symbol names verified per account). That path stays
on a **v20 (NETTING)** account + REST API. **Do not** move Tradex to OANDA One unless
the product mix changes to Share CFDs (would require an MT5/broker redesign).

OANDA One is relevant only if we later add share-CFD strategies.

## Email body (filed)

> Dear y m,
>
> Thank you for contacting OANDA.
> I hope this email finds you well.
>
> I wanted to follow up on our previous conversation regarding your demo trading
> setup, as I want to make sure you have all the information you need to explore
> our platforms.
>
> We understand you are eager to trade Share CFDs and are looking for options
> regarding ETF CFDs. Please find the detailed guidelines below addressing your
> inquiries:
>
> **1. Why You Cannot See Share CFDs on MT4**
> Please be advised that our MetaTrader 4 (MT4) platform does not support Share
> CFDs. To view and trade Share CFDs within our Australian division, you will need
> to utilize an OANDA One sub-account.
>
> With an OANDA One sub-account, you can easily access Share CFDs across the
> following platforms:
> - OANDA mobile app
> - MetaTrader 5 (desktop, web, tablet, and mobile)
> - TradingView (desktop, web, and mobile)
>
> **2. ETF CFDs Availability in Australia**
> We would like to clarify that we do not support or offer ETF CFDs. However, we
> do offer a wide variety of other asset classes for you to trade. The available
> CFD instruments in your region include:
> - Forex CFDs
> - Index CFDs
> - Metals CFDs
> - Commodity CFDs
> - Bond CFDs
> - Crypto CFDs
> - Share CFDs (via OANDA One sub-account)
>
> **3. How to Create an OANDA One Sub-Account**
> If you would like to set up an OANDA One sub-account, you can easily do so by
> following these steps:
> 1. Log in to your OANDA Hub portal.
> 2. Under the Accounts section, click ADD NEW ACCOUNT.
> 3. Toggle the selection to choose your account type and select OANDA One (this
>    is different from standard v20 or v20 MT4 accounts).
> 4. Choose your preferred account currency.
> 5. Create a password for this new sub-account.
> 6. Click CREATE.
>
> Once created, you will be able to search for and view Share CFDs on your demo
> profile.
>
> **Helpful Resources**
> - Share CFDs:
>   https://help.oanda.com/au/en/faqs/trade-share-cfds.htm
> - Margin rates / instruments:
>   https://www.oanda.com/au-en/legal/margin-rates/
> - Create sub-accounts:
>   https://help.oanda.com/au/en/faqs/create-additional-sub-accounts.htm
>
> It is my pleasure to assist you,
> Ken Leander
> Client Experience Team

## Related local checks

- Practice v20 EU account `101-011-39706277-003` lists index CFDs including
  `DE30_EUR`, `FR40_EUR`, `EU50_EUR` via
  `GET /v3/accounts/{id}/instruments` (confirm tradeable in-session on a weekday).
- Runtime configs/specs use **`DE30_EUR`** (DAX / Germany 40 CFD on AU practice).
  Older notes may still say `DE40_EUR`.
