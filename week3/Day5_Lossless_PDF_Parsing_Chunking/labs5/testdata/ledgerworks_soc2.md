# LEDGERWORKS — Vendor Assurance Review Q3 2025

Навчальний документ курсу AI Agents Engineering. Усі компанії, числа й підписанти вигадані.

## 1 Scope

Огляд охоплює звіти SOC2 Type II трьох підрядників LEDGERWORKS за період 01.07.2025–30.09.2025.
Мета — визначити знахідки з Severity=High, які потребують плану виправлення до 31.12.2025.

### 1.1 Vendors in scope

| Vendor | Service | Report | Period | Auditor |
|--------|---------|--------|--------|---------|
| Acme Bank JSC | Card acquiring | SOC2 Type II | Q3 2025 | Kovalenko & Partners |
| Northwind Cloud | Hosting | SOC2 Type II | Q3 2025 | Delta Assurance |
| Globex Payments | Payouts | SOC2 Type I | Q3 2025 | Kovalenko & Partners |

### 1.2 Method

Кожну знахідку звірено з контрольною метою (Control ID) звіту. Знахідки без Control ID не враховуються.

## 2 Findings

### 2.1 Security Findings

| Finding ID | Vendor | Control ID | Severity | Status |
|------------|--------|------------|----------|--------|
| F-101 | Acme Bank JSC | CC6.1 | High | Open |
| F-102 | Acme Bank JSC | CC7.2 | Medium | Remediated |
| F-103 | Northwind Cloud | CC6.6 | High | Open |
| F-104 | Acme Bank JSC | CC8.1 | High | In progress |
| F-105 | Globex Payments | CC6.1 | Low | Open |

Знахідка F-101: привілейовані облікові записи Acme Bank JSC не проходили квартальний перегляд доступу.
Знахідка F-104: зміни в продуктивному середовищі розгорталися без затвердження другої особи.

### 2.2 Availability Findings

Northwind Cloud зафіксував два інциденти недоступності понад 4 години. Обидва закриті з RCA.

## 3 Tariffs and Signers

### 3.1 Merchant tariffs

| Merchant | Tariff | Fee, % | NBU 2026 requirement | Contract |
|----------|--------|--------|----------------------|----------|
| A-114 | T-2 | 2.9 | Yes | CT-2025-014 |
| A-207 | T-2 | 2.9 | Yes | CT-2025-031 |
| A-331 | T-1 | 1.8 | No | CT-2025-007 |

### 3.2 Contract signers

- CT-2025-014 — підписав Олег Мельник, CFO, 12.03.2025.
- CT-2025-031 — підписала Ірина Бойко, Head of Partnerships, 02.06.2025.
- CT-2025-007 — підписав Олег Мельник, CFO, 20.01.2025.

## 4 Conclusion

Три знахідки з Severity=High: дві стосуються Acme Bank JSC (F-101, F-104), одна — Northwind Cloud (F-103).
