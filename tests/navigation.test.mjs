import { describe, expect, it } from "vitest";
import { TestDriver } from "testdriverai/vitest/hooks";

// Sample TestDriver test for the AXIS MCP production site (https://axismcp.app).
// Verifies in-page navigation from the landing page to the Pricing section.
describe("AXIS MCP — navigation", () => {
  it("navigates to the Pricing section from the header", async (context) => {
    const testdriver = TestDriver(context);

    await testdriver.provision.chrome({ url: "https://axismcp.app" });
    await testdriver.wait(3000);

    // Click the Pricing link in the top navigation.
    await testdriver.find('The "Pricing" navigation link in the top header').click();
    await testdriver.wait(3000);

    const assertResult = await testdriver.assert(
      "The pricing section is visible with multiple plans including a Free plan and a Pro plan showing monthly prices",
    );
    expect(assertResult).toBeTruthy();
  });
});
